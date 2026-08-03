// Package api provides the self-hosted control-plane HTTP API.
// v1.2.1: tenant-aware identity, mandatory workspace, immutable policy snapshots.
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/authn"
	"github.com/justrunme/architecture-rehearsal/internal/authz"
	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/contract"
	"github.com/justrunme/architecture-rehearsal/internal/persist"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

// Server is the control-plane API gateway.
type Server struct {
	Backend     Backend
	AuthN       authn.Authenticator
	AuthZ       *authz.Authorizer
	WorkDirRoot string
	// Async: when true, advance enqueues jobs instead of sync execute.
	Async bool
	// RequireWorkDir: production serve sets true; tests may use temp dir via Options.
	RequireWorkDir bool
	// Metrics
	reqTotal  atomic.Int64
	reqErrors atomic.Int64
	startedAt time.Time
}

// Options for NewServerWith.
type Options struct {
	AuthN          authn.Authenticator
	Backend        Backend
	WorkDirRoot    string
	Async          bool
	RequireWorkDir bool
}

// NewServer constructs a test server (memory + temp workdir + insecure local-dev).
func NewServer() *Server {
	dir, err := os.MkdirTemp("", "rehearsal-api-*")
	if err != nil {
		dir = os.TempDir()
	}
	return NewServerWith(Options{
		AuthN:       authn.Default(),
		Backend:     NewMemoryBackend(),
		WorkDirRoot: dir,
	})
}

// NewServerWith allows production wiring.
func NewServerWith(opts Options) *Server {
	a := opts.AuthN
	if a == nil {
		a = authn.Default()
	}
	b := opts.Backend
	if b == nil {
		b = NewMemoryBackend()
	}
	return &Server{
		Backend:        b,
		AuthN:          a,
		AuthZ:          authz.Default(),
		WorkDirRoot:    opts.WorkDirRoot,
		Async:          opts.Async,
		RequireWorkDir: opts.RequireWorkDir,
		startedAt:      time.Now().UTC(),
	}
}

// Handler returns the HTTP mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.wrap(s.healthz))
	mux.HandleFunc("GET /readyz", s.wrap(s.readyz))
	mux.HandleFunc("GET /v1/version", s.wrap(s.version))
	mux.HandleFunc("GET /v1/metrics", s.wrap(s.metrics))
	mux.HandleFunc("POST /v1/runs", s.withAuth(authz.ActionRunCreate, s.createRun))
	mux.HandleFunc("GET /v1/runs", s.withAuth(authz.ActionRunRead, s.listRuns))
	mux.HandleFunc("GET /v1/runs/{id}", s.withAuth(authz.ActionRunRead, s.getRun))
	mux.HandleFunc("POST /v1/runs/{id}/advance", s.withAuth(authz.ActionRunWrite, s.advanceRun))
	mux.HandleFunc("GET /v1/runs/{id}/evidence", s.withAuth(authz.ActionEvidenceRead, s.getEvidence))
	mux.HandleFunc("POST /v1/clusters", s.withAuth(authz.ActionClusterWrite, s.createCluster))
	mux.HandleFunc("GET /v1/clusters", s.withAuth(authz.ActionClusterRead, s.listClusters))
	mux.HandleFunc("POST /v1/policies", s.withAuth(authz.ActionPolicyWrite, s.createPolicy))
	mux.HandleFunc("GET /v1/policies", s.withAuth(authz.ActionPolicyRead, s.listPolicies))
	mux.HandleFunc("GET /v1/calibration", s.withAuth(authz.ActionRunRead, s.getCalibration))
	mux.HandleFunc("GET /v1/schemas", s.wrap(s.listSchemas))
	return mux
}

func (s *Server) wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.reqTotal.Add(1)
		h(w, r)
	}
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, _ *http.Request) {
	if s.RequireWorkDir && s.WorkDirRoot == "" {
		http.Error(w, "workdir required", 503)
		return
	}
	if err := s.Backend.Ready(); err != nil {
		http.Error(w, "backend not ready: "+err.Error(), 503)
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ready", "async": s.Async})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{
		"version":    "1.2.1",
		"apiVersion": contract.APIVersionV1,
		"product":    "architecture-rehearsal",
	})
}

func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	p, l, d, f := s.Backend.JobStats()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP rehearsal_requests_total Total HTTP requests\n")
	fmt.Fprintf(w, "# TYPE rehearsal_requests_total counter\n")
	fmt.Fprintf(w, "rehearsal_requests_total %d\n", s.reqTotal.Load())
	fmt.Fprintf(w, "# HELP rehearsal_request_errors_total Total HTTP error responses tracked\n")
	fmt.Fprintf(w, "# TYPE rehearsal_request_errors_total counter\n")
	fmt.Fprintf(w, "rehearsal_request_errors_total %d\n", s.reqErrors.Load())
	fmt.Fprintf(w, "# HELP rehearsal_jobs Jobs by status\n")
	fmt.Fprintf(w, "# TYPE rehearsal_jobs gauge\n")
	fmt.Fprintf(w, "rehearsal_jobs{status=\"pending\"} %d\n", p)
	fmt.Fprintf(w, "rehearsal_jobs{status=\"leased\"} %d\n", l)
	fmt.Fprintf(w, "rehearsal_jobs{status=\"done\"} %d\n", d)
	fmt.Fprintf(w, "rehearsal_jobs{status=\"failed\"} %d\n", f)
	fmt.Fprintf(w, "# HELP rehearsal_uptime_seconds Process uptime\n")
	fmt.Fprintf(w, "# TYPE rehearsal_uptime_seconds gauge\n")
	fmt.Fprintf(w, "rehearsal_uptime_seconds %d\n", int(time.Since(s.startedAt).Seconds()))
}

func (s *Server) listSchemas(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, contract.Catalog())
}

type createRunReq struct {
	ID             string   `json:"id"`
	IdempotencyKey string   `json:"idempotencyKey"`
	ClusterName    string   `json:"clusterName"`
	BaselineRef    string   `json:"baselineRef"`
	ChangeRef      string   `json:"changeRef"`
	ObservedRef    string   `json:"observedRef"`
	Scenarios      []string `json:"scenarios"`
	Org            string   `json:"org"`
	Project        string   `json:"project"`
	Environment    string   `json:"environment"`
	PolicyID       string   `json:"policyId"`
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	var req createRunReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		s.reqErrors.Add(1)
		http.Error(w, err.Error(), 400)
		return
	}
	org := actor.Org
	if org == "" {
		http.Error(w, "principal has no org", 403)
		return
	}
	_ = req.Org // client org claim ignored
	if !s.AuthZ.Allow(actor, authz.ActionRunCreate, authz.Resource{Org: org, Project: req.Project, Environment: req.Environment}) {
		http.Error(w, "forbidden", 403)
		return
	}
	if req.IdempotencyKey != "" {
		if existing, _ := s.Backend.GetRunByIdempotency(org, req.IdempotencyKey); existing != nil {
			writeJSON(w, 200, existing)
			return
		}
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("run-%d", time.Now().UTC().UnixNano())
	}
	// Conflict if (org, id) already exists
	if existing, _ := s.Backend.GetRun(org, req.ID); existing != nil {
		s.reqErrors.Add(1)
		http.Error(w, "run id already exists", 409)
		return
	}

	baseRef, err := s.sandboxPath(req.BaselineRef)
	if err != nil {
		http.Error(w, "baselineRef: "+err.Error(), 400)
		return
	}
	changeRef, err := s.sandboxPath(req.ChangeRef)
	if err != nil {
		http.Error(w, "changeRef: "+err.Error(), 400)
		return
	}
	obsRef := req.ObservedRef
	if obsRef != "" {
		obsRef, err = s.sandboxPath(obsRef)
		if err != nil {
			http.Error(w, "observedRef: "+err.Error(), 400)
			return
		}
	}

	// Immutable policy snapshot per run (digest-named file).
	policyPath, policyDigest := s.materializePolicySnapshot(org, req.ID)

	rr := run.NewRun(req.ID, req.IdempotencyKey, run.Spec{
		ClusterName:    req.ClusterName,
		BaselineRef:    baseRef,
		ChangeRef:      changeRef,
		ObservedRef:    obsRef,
		Scenarios:      req.Scenarios,
		PolicyPath:     policyPath,
		OutDir:         filepath.Join("out", org, req.ID),
		TimeoutSeconds: 600,
	})
	rr.Labels = map[string]string{
		"org": org, "project": req.Project, "environment": req.Environment,
		"actor": actor.ID,
	}
	if policyDigest != "" {
		rr.Labels["policyDigest"] = policyDigest
	}
	if err := s.Backend.CreateRun(rr); err != nil {
		s.reqErrors.Add(1)
		if errors.Is(err, ErrConflict) {
			http.Error(w, "run id or idempotency key conflict", 409)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	s.Backend.Audit(actor.ID, "run.create", rr.ID, "org="+org, org)
	writeJSON(w, 201, rr)
}

// materializePolicySnapshot writes a content-addressed policy file for this run only.
func (s *Server) materializePolicySnapshot(org, runID string) (path, digest string) {
	if s.WorkDirRoot == "" {
		return "", ""
	}
	pol, _ := s.Backend.GetOrgPolicy(org)
	if pol == nil {
		return "", ""
	}
	rawYAML, err := policyYAMLBytes(pol)
	if err != nil {
		return "", ""
	}
	sum := sha256.Sum256(rawYAML)
	digest = hex.EncodeToString(sum[:])
	pdir := filepath.Join(s.WorkDirRoot, "policies", org, "snapshots")
	_ = os.MkdirAll(pdir, 0o755)
	path = filepath.Join(pdir, digest[:16]+"-"+runID+".yaml")
	if _, err := os.Stat(path); err != nil {
		_ = os.WriteFile(path, rawYAML, 0o644)
	}
	return path, digest
}

func policyYAMLBytes(pol map[string]any) ([]byte, error) {
	var b strings.Builder
	b.WriteString("apiVersion: rehearsal.io/v1beta1\nkind: RehearsalPolicy\n")
	if v, ok := pol["unknownAsBlock"].(bool); ok && v {
		b.WriteString("unknownAsBlock: true\n")
	}
	b.WriteString("block:\n")
	if arr, ok := pol["block"].([]any); ok {
		for _, x := range arr {
			b.WriteString("  - " + fmt.Sprint(x) + "\n")
		}
	} else {
		b.WriteString("  - risk in [\"critical\",\"high\"]\n  - decision == \"block\"\n")
	}
	b.WriteString("warn:\n")
	if arr, ok := pol["warn"].([]any); ok {
		for _, x := range arr {
			b.WriteString("  - " + fmt.Sprint(x) + "\n")
		}
	} else {
		b.WriteString("  - risk == \"medium\"\n")
	}
	return []byte(b.String()), nil
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	all, err := s.Backend.ListRuns(actor.Org)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var out []*run.RehearsalRun
	for _, rr := range all {
		if !s.AuthZ.CanAccessObject(actor, authz.ActionRunRead, resourceFromRun(rr)) {
			continue
		}
		out = append(out, rr)
	}
	writeJSON(w, 200, out)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	rr, err := s.Backend.GetRun(actor.Org, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if rr == nil || !s.AuthZ.CanAccessObject(actor, authz.ActionRunRead, resourceFromRun(rr)) {
		http.Error(w, "not found", 404)
		return
	}
	writeJSON(w, 200, rr)
}

func (s *Server) advanceRun(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	rr, err := s.Backend.GetRun(actor.Org, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if rr == nil || !s.AuthZ.CanAccessObject(actor, authz.ActionRunWrite, resourceFromRun(rr)) {
		http.Error(w, "not found", 404)
		return
	}
	var body struct {
		WorkDir string `json:"workDir"`
		Action  string `json:"action"`
		Async   *bool  `json:"async"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)

	async := s.Async
	if body.Async != nil {
		async = *body.Async
	}

	// Cancel: update run + cancel pending/leased jobs
	if body.Action == "cancel" {
		_ = rr.Transition(run.PhaseCancelled, "cancelled by "+actor.ID)
		_ = s.Backend.UpdateRun(rr)
		n, _ := s.Backend.CancelJobsForRun(actor.Org, rr.ID)
		s.Backend.Audit(actor.ID, "run.cancel", rr.ID, fmt.Sprintf("jobs=%d", n), actor.Org)
		writeJSON(w, 200, map[string]any{"run": rr, "jobsCancelled": n})
		return
	}

	if async {
		wd := body.WorkDir
		if wd != "" {
			wd, err = s.sandboxPath(wd)
			if err != nil {
				http.Error(w, "workDir: "+err.Error(), 400)
				return
			}
		}
		payload, _ := json.Marshal(persist.AdvancePayload{WorkDir: wd, Action: body.Action})
		opID := fmt.Sprintf("advance:%s:%s", actor.Org, rr.ID)
		jobID, err := s.Backend.Enqueue(persist.JobAdvanceRun, rr.ID, actor.Org, string(payload), opID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		s.Backend.Audit(actor.ID, "run.enqueue", rr.ID, jobID, actor.Org)
		writeJSON(w, 202, map[string]any{"runId": rr.ID, "jobId": jobID, "status": "queued"})
		return
	}

	// Sync path
	wd := body.WorkDir
	if wd != "" {
		wd, err = s.sandboxPath(wd)
		if err != nil {
			http.Error(w, "workDir: "+err.Error(), 400)
			return
		}
	} else if s.WorkDirRoot != "" {
		wd = s.WorkDirRoot
	}
	eng := &run.Engine{WorkDir: wd, Holder: actor.ID}
	if err := eng.Execute(rr); err != nil {
		_ = s.Backend.UpdateRun(rr)
		writeJSON(w, 200, map[string]any{"run": rr, "error": err.Error()})
		return
	}
	_ = s.Backend.UpdateRun(rr)
	s.recordCal(actor.Org, rr)
	s.Backend.Audit(actor.ID, "run.advance", rr.ID, string(rr.Status.Phase), actor.Org)
	writeJSON(w, 200, rr)
}

func (s *Server) recordCal(org string, rr *run.RehearsalRun) {
	if len(rr.Status.PredictedFailures) == 0 {
		return
	}
	verified := rr.Status.VerifyOutcome != "" && rr.Status.VerifyOutcome != "inconclusive"
	for _, sc := range rr.Status.PredictedFailures {
		obsOK := rr.Status.VerifyOutcome == "verified"
		_ = s.Backend.RecordCalibration(org, calibrate.Outcome{
			Scenario: sc, Predicted: true, Observed: obsOK, Verified: verified,
		})
	}
}

func (s *Server) getEvidence(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	rr, err := s.Backend.GetRun(actor.Org, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if rr == nil || !s.AuthZ.CanAccessObject(actor, authz.ActionEvidenceRead, resourceFromRun(rr)) {
		http.Error(w, "not found", 404)
		return
	}
	out := map[string]any{
		"runId":             rr.ID,
		"org":               rr.Labels["org"],
		"digests":           rr.Digests,
		"phase":             rr.Status.Phase,
		"verifyOutcome":     rr.Status.VerifyOutcome,
		"chainPath":         rr.Status.ChainPath,
		"predictedFailures": rr.Status.PredictedFailures,
		"policyDigest":      rr.Labels["policyDigest"],
	}
	if rr.Status.ChainPath != "" {
		if raw, err := os.ReadFile(rr.Status.ChainPath); err == nil {
			var chain any
			if json.Unmarshal(raw, &chain) == nil {
				out["chain"] = chain
			}
		}
		dssePath := filepath.Join(filepath.Dir(rr.Status.ChainPath), "evidence-dsse.json")
		if raw, err := os.ReadFile(dssePath); err == nil {
			var env any
			if json.Unmarshal(raw, &env) == nil {
				out["dsse"] = env
			}
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) createCluster(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	var c Cluster
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&c); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if c.Name == "" {
		http.Error(w, "name required", 400)
		return
	}
	c.Org = actor.Org
	if c.Org == "" {
		http.Error(w, "principal has no org", 403)
		return
	}
	if !s.AuthZ.Allow(actor, authz.ActionClusterWrite, authz.Resource{Org: c.Org}) {
		http.Error(w, "forbidden", 403)
		return
	}
	c.CreatedAt = time.Now().UTC()
	if err := s.Backend.PutCluster(c); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Backend.Audit(actor.ID, "cluster.create", c.Name, "org="+c.Org, c.Org)
	writeJSON(w, 201, c)
}

func (s *Server) listClusters(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	all, err := s.Backend.ListClusters(actor.Org)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var out []Cluster
	for _, c := range all {
		if !s.AuthZ.CanAccessObject(actor, authz.ActionClusterRead, authz.Resource{Org: c.Org}) {
			continue
		}
		out = append(out, c)
	}
	writeJSON(w, 200, out)
}

func (s *Server) createPolicy(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	var p map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	id, _ := p["id"].(string)
	if id == "" {
		id = fmt.Sprintf("policy-%d", time.Now().UnixNano())
	}
	if actor.Org == "" {
		http.Error(w, "principal has no org", 403)
		return
	}
	if !s.AuthZ.Allow(actor, authz.ActionPolicyWrite, authz.Resource{Org: actor.Org}) {
		http.Error(w, "forbidden", 403)
		return
	}
	if err := s.Backend.PutPolicy(id, actor.Org, p); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Backend.Audit(actor.ID, "policy.create", id, "org="+actor.Org, actor.Org)
	p["id"] = id
	p["org"] = actor.Org
	writeJSON(w, 201, p)
}

func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	all, err := s.Backend.ListPolicies(actor.Org)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, all)
}

func (s *Server) getCalibration(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	if actor.Org == "" || !s.AuthZ.Allow(actor, authz.ActionRunRead, authz.Resource{Org: actor.Org}) {
		http.Error(w, "forbidden", 403)
		return
	}
	writeJSON(w, 200, map[string]any{"org": actor.Org, "report": s.Backend.CalibrationReport(actor.Org)})
}

type handlerFunc func(http.ResponseWriter, *http.Request, authn.Principal)

func (s *Server) withAuth(action authz.Action, next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.reqTotal.Add(1)
		p, err := s.AuthN.Authenticate(r)
		if err != nil {
			s.reqErrors.Add(1)
			http.Error(w, "unauthorized: "+err.Error(), 401)
			return
		}
		res := authz.Resource{Org: p.Org}
		if !s.AuthZ.Allow(p, action, res) {
			s.reqErrors.Add(1)
			http.Error(w, "forbidden", 403)
			return
		}
		next(w, r, p)
	}
}

func resourceFromRun(rr *run.RehearsalRun) authz.Resource {
	if rr == nil {
		return authz.Resource{}
	}
	return authz.Resource{
		Org:         rr.Labels["org"],
		Project:     rr.Labels["project"],
		Environment: rr.Labels["environment"],
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// Cluster is a registered cluster connection (no raw credentials).
type Cluster struct {
	Name          string    `json:"name"`
	Org           string    `json:"org,omitempty"`
	KubeconfigRef string    `json:"kubeconfigRef,omitempty"`
	Server        string    `json:"server,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}
