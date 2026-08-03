// Package api provides the self-hosted control-plane HTTP API.
// v1.0.1: tenant isolation, secure token bootstrap, object-level authz, WorkDir sandbox.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/authn"
	"github.com/justrunme/architecture-rehearsal/internal/authz"
	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/contract"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

// Server is the control-plane API gateway.
type Server struct {
	Store     *MemoryStore
	AuthN     authn.Authenticator
	AuthZ     *authz.Authorizer
	Calibrate *calibrate.Store
	// WorkDirRoot if set, client WorkDir must stay within this directory.
	WorkDirRoot string
	mu          sync.Mutex
}

// Options for NewServer.
type Options struct {
	AuthN       authn.Authenticator
	WorkDirRoot string
}

// NewServer constructs a control plane (tests may use insecure Default auth).
func NewServer() *Server {
	return NewServerWith(Options{AuthN: authn.Default()})
}

// NewServerWith allows production wiring (FromEnv authenticator + workspace root).
func NewServerWith(opts Options) *Server {
	a := opts.AuthN
	if a == nil {
		a = authn.Default()
	}
	return &Server{
		Store:       NewMemoryStore(),
		AuthN:       a,
		AuthZ:       authz.Default(),
		Calibrate:   calibrate.NewStore(),
		WorkDirRoot: opts.WorkDirRoot,
	}
}

// Handler returns the HTTP mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.healthz)
	mux.HandleFunc("GET /v1/version", s.version)
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
	mux.HandleFunc("GET /v1/schemas", s.listSchemas)
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{
		"version":    "1.1.0",
		"apiVersion": contract.APIVersionV1,
		"product":    "architecture-rehearsal",
	})
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
	// Org in body is IGNORED for tenant binding — principal.Org is authoritative.
	Org         string `json:"org"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	var req createRunReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Tenant: force org from principal; body/header org is ignored (not trusted).
	org := actor.Org
	if org == "" {
		http.Error(w, "principal has no org", 403)
		return
	}
	_ = req.Org // deliberately discarded
	// Authorize create in principal's org
	if !s.AuthZ.Allow(actor, authz.ActionRunCreate, authz.Resource{Org: org, Project: req.Project, Environment: req.Environment}) {
		http.Error(w, "forbidden", 403)
		return
	}

	if req.IdempotencyKey != "" {
		if existing := s.Store.GetByIdempotency(req.IdempotencyKey); existing != nil {
			if !s.AuthZ.CanAccessObject(actor, authz.ActionRunRead, resourceFromRun(existing)) {
				http.Error(w, "forbidden", 403)
				return
			}
			writeJSON(w, 200, existing)
			return
		}
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("run-%d", time.Now().UTC().UnixNano())
	}
	// Sandbox file refs
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

	rr := run.NewRun(req.ID, req.IdempotencyKey, run.Spec{
		ClusterName:    req.ClusterName,
		BaselineRef:    baseRef,
		ChangeRef:      changeRef,
		ObservedRef:    obsRef,
		Scenarios:      req.Scenarios,
		TimeoutSeconds: 600,
	})
	rr.Labels = map[string]string{
		"org": org, "project": req.Project, "environment": req.Environment,
		"actor": actor.ID,
	}
	s.Store.Put(rr)
	s.Store.Audit(actor.ID, "run.create", rr.ID, "org="+org)
	writeJSON(w, 201, rr)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	all := s.Store.ListRuns()
	var out []*run.RehearsalRun
	for _, rr := range all {
		res := resourceFromRun(rr)
		if !s.AuthZ.CanAccessObject(actor, authz.ActionRunRead, res) {
			continue
		}
		out = append(out, rr)
	}
	writeJSON(w, 200, out)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	rr := s.Store.Get(r.PathValue("id"))
	if rr == nil {
		http.Error(w, "not found", 404)
		return
	}
	if !s.AuthZ.CanAccessObject(actor, authz.ActionRunRead, resourceFromRun(rr)) {
		// Do not leak existence across tenants
		http.Error(w, "not found", 404)
		return
	}
	writeJSON(w, 200, rr)
}

func (s *Server) advanceRun(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	rr := s.Store.Get(r.PathValue("id"))
	if rr == nil {
		http.Error(w, "not found", 404)
		return
	}
	if !s.AuthZ.CanAccessObject(actor, authz.ActionRunWrite, resourceFromRun(rr)) {
		http.Error(w, "not found", 404)
		return
	}
	var body struct {
		WorkDir string `json:"workDir"`
		Action  string `json:"action"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
	if body.Action == "cancel" {
		_ = rr.Transition(run.PhaseCancelled, "cancelled by "+actor.ID)
		s.Store.Put(rr)
		writeJSON(w, 200, rr)
		return
	}
	wd := body.WorkDir
	if wd != "" {
		var err error
		wd, err = s.sandboxPath(wd)
		if err != nil {
			http.Error(w, "workDir: "+err.Error(), 400)
			return
		}
	} else if s.WorkDirRoot != "" {
		wd = s.WorkDirRoot
	}
	eng := &run.Engine{WorkDir: wd, Holder: actor.ID, Calibrate: s.Calibrate}
	if err := eng.Execute(rr); err != nil {
		s.Store.Put(rr)
		writeJSON(w, 200, map[string]any{"run": rr, "error": err.Error()})
		return
	}
	s.Store.Put(rr)
	s.Store.Audit(actor.ID, "run.advance", rr.ID, string(rr.Status.Phase))
	writeJSON(w, 200, rr)
}

func (s *Server) getEvidence(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	rr := s.Store.Get(r.PathValue("id"))
	if rr == nil {
		http.Error(w, "not found", 404)
		return
	}
	if !s.AuthZ.CanAccessObject(actor, authz.ActionEvidenceRead, resourceFromRun(rr)) {
		http.Error(w, "not found", 404)
		return
	}
	out := map[string]any{
		"runId":            rr.ID,
		"org":              rr.Labels["org"],
		"digests":          rr.Digests,
		"phase":            rr.Status.Phase,
		"verifyOutcome":    rr.Status.VerifyOutcome,
		"chainPath":        rr.Status.ChainPath,
		"predictedFailures": rr.Status.PredictedFailures,
	}
	// Inline chain if persisted
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
	// Force tenant from principal
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
	s.Store.PutCluster(c)
	s.Store.Audit(actor.ID, "cluster.create", c.Name, "org="+c.Org)
	writeJSON(w, 201, c)
}

func (s *Server) listClusters(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	var out []Cluster
	for _, c := range s.Store.ListClusters() {
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
		p["id"] = id
	}
	// Force org
	p["org"] = actor.Org
	if actor.Org == "" {
		http.Error(w, "principal has no org", 403)
		return
	}
	if !s.AuthZ.Allow(actor, authz.ActionPolicyWrite, authz.Resource{Org: actor.Org}) {
		http.Error(w, "forbidden", 403)
		return
	}
	s.Store.PutPolicy(id, p)
	s.Store.Audit(actor.ID, "policy.create", id, "org="+actor.Org)
	writeJSON(w, 201, p)
}

func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	var out []map[string]any
	for _, p := range s.Store.ListPolicies() {
		org, _ := p["org"].(string)
		if !s.AuthZ.CanAccessObject(actor, authz.ActionPolicyRead, authz.Resource{Org: org}) {
			continue
		}
		out = append(out, p)
	}
	writeJSON(w, 200, out)
}

func (s *Server) getCalibration(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	// Calibration is org-scoped in labels when recorded; report is global model for now —
	// only platform-admin or same-org operators may read (require org-bound principal).
	if actor.Org == "" {
		http.Error(w, "forbidden", 403)
		return
	}
	// Non-admin: still allow read of aggregate (no per-run PII) but only if authenticated.
	if !s.AuthZ.Allow(actor, authz.ActionRunRead, authz.Resource{Org: actor.Org}) {
		http.Error(w, "forbidden", 403)
		return
	}
	rep := s.Calibrate.Report()
	writeJSON(w, 200, map[string]any{"org": actor.Org, "report": rep})
}

type handlerFunc func(http.ResponseWriter, *http.Request, authn.Principal)

func (s *Server) withAuth(action authz.Action, next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.AuthN.Authenticate(r)
		if err != nil {
			http.Error(w, "unauthorized: "+err.Error(), 401)
			return
		}
		// Authorization for collection endpoints uses principal org only.
		// Object endpoints re-check after load.
		res := authz.Resource{Org: p.Org}
		if !s.AuthZ.Allow(p, action, res) {
			http.Error(w, "forbidden", 403)
			return
		}
		next(w, r, p)
	}
}

// sandboxPath ensures path is under WorkDirRoot when configured.
func (s *Server) sandboxPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if s.WorkDirRoot == "" {
		// Production serve should set WorkDirRoot; without it, reject absolute escapes of ".."
		clean := filepath.Clean(p)
		if strings.Contains(clean, "..") {
			return "", fmt.Errorf("path must not contain ..")
		}
		return clean, nil
	}
	root, err := filepath.Abs(s.WorkDirRoot)
	if err != nil {
		return "", err
	}
	// Relative paths join root
	target := p
	if !filepath.IsAbs(p) {
		target = filepath.Join(root, p)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path outside workspace root")
	}
	return abs, nil
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
