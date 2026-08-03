// Package api provides the self-hosted control-plane HTTP API (v0.11).
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	mu        sync.Mutex
}

// NewServer constructs a default in-memory control plane.
func NewServer() *Server {
	return &Server{
		Store:     NewMemoryStore(),
		AuthN:     authn.Default(),
		AuthZ:     authz.Default(),
		Calibrate: calibrate.NewStore(),
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
	mux.HandleFunc("GET /v1/calibration", s.withAuth(authz.ActionRunRead, s.getCalibration))
	mux.HandleFunc("GET /v1/schemas", s.listSchemas)
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{
		"version":    "1.0.0",
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
	Org            string   `json:"org"`
	Project        string   `json:"project"`
	Environment    string   `json:"environment"`
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	var req createRunReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Idempotency
	if req.IdempotencyKey != "" {
		if existing := s.Store.GetByIdempotency(req.IdempotencyKey); existing != nil {
			writeJSON(w, 200, existing)
			return
		}
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("run-%d", time.Now().UTC().UnixNano())
	}
	rr := run.NewRun(req.ID, req.IdempotencyKey, run.Spec{
		ClusterName: req.ClusterName,
		BaselineRef: req.BaselineRef,
		ChangeRef:   req.ChangeRef,
		ObservedRef: req.ObservedRef,
		Scenarios:   req.Scenarios,
		TimeoutSeconds: 600,
	})
	rr.Labels = map[string]string{
		"org": req.Org, "project": req.Project, "environment": req.Environment,
		"actor": actor.ID,
	}
	s.Store.Put(rr)
	s.Store.Audit(actor.ID, "run.create", rr.ID, "")
	writeJSON(w, 201, rr)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	org := r.URL.Query().Get("org")
	all := s.Store.ListRuns()
	var out []*run.RehearsalRun
	for _, rr := range all {
		if org != "" && rr.Labels["org"] != org {
			continue
		}
		if !s.AuthZ.Allow(actor, authz.ActionRunRead, authz.Resource{
			Org: rr.Labels["org"], Project: rr.Labels["project"], Environment: rr.Labels["environment"],
		}) {
			continue
		}
		out = append(out, rr)
	}
	writeJSON(w, 200, out)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	id := r.PathValue("id")
	rr := s.Store.Get(id)
	if rr == nil {
		http.Error(w, "not found", 404)
		return
	}
	writeJSON(w, 200, rr)
}

func (s *Server) advanceRun(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	id := r.PathValue("id")
	rr := s.Store.Get(id)
	if rr == nil {
		http.Error(w, "not found", 404)
		return
	}
	// Optional execute engine if workdir provided
	var body struct {
		WorkDir string `json:"workDir"`
		Action  string `json:"action"` // execute|cancel
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Action == "cancel" {
		_ = rr.Transition(run.PhaseCancelled, "cancelled by "+actor.ID)
		s.Store.Put(rr)
		writeJSON(w, 200, rr)
		return
	}
	eng := &run.Engine{WorkDir: body.WorkDir, Holder: actor.ID}
	if err := eng.Execute(rr); err != nil {
		s.Store.Put(rr)
		writeJSON(w, 200, map[string]any{"run": rr, "error": err.Error()})
		return
	}
	s.Store.Put(rr)
	// Feed calibration if completed
	if rr.Status.Phase == run.PhaseCompleted || rr.Status.Phase == run.PhaseFailed || rr.Status.Phase == run.PhaseInconclusive {
		s.Calibrate.Record(calibrate.Outcome{
			Scenario:  "gate",
			Predicted: rr.Status.Decision != "approve",
			Observed:  rr.Status.Phase == run.PhaseFailed || stringsContains(rr.Status.Message, "diverged"),
			Verified:  rr.Status.Phase == run.PhaseCompleted,
		})
	}
	s.Store.Audit(actor.ID, "run.advance", rr.ID, string(rr.Status.Phase))
	writeJSON(w, 200, rr)
}

func (s *Server) getEvidence(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	id := r.PathValue("id")
	rr := s.Store.Get(id)
	if rr == nil {
		http.Error(w, "not found", 404)
		return
	}
	writeJSON(w, 200, map[string]any{
		"runId":   rr.ID,
		"digests": rr.Digests,
		"phase":   rr.Status.Phase,
	})
}

func (s *Server) createCluster(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	var c Cluster
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if c.Name == "" {
		http.Error(w, "name required", 400)
		return
	}
	// Never store raw kubeconfig secrets in logs; accept ref only.
	c.CreatedAt = time.Now().UTC()
	s.Store.PutCluster(c)
	s.Store.Audit(actor.ID, "cluster.create", c.Name, "")
	writeJSON(w, 201, c)
}

func (s *Server) listClusters(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	writeJSON(w, 200, s.Store.ListClusters())
}

func (s *Server) createPolicy(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	var p map[string]any
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	id, _ := p["id"].(string)
	if id == "" {
		id = fmt.Sprintf("policy-%d", time.Now().UnixNano())
		p["id"] = id
	}
	s.Store.PutPolicy(id, p)
	s.Store.Audit(actor.ID, "policy.create", id, "")
	writeJSON(w, 201, p)
}

func (s *Server) getCalibration(w http.ResponseWriter, r *http.Request, actor authn.Principal) {
	writeJSON(w, 200, s.Calibrate.Report())
}

type handlerFunc func(http.ResponseWriter, *http.Request, authn.Principal)

func (s *Server) withAuth(action authz.Action, next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.AuthN.Authenticate(r)
		if err != nil {
			http.Error(w, "unauthorized: "+err.Error(), 401)
			return
		}
		res := authz.Resource{
			Org:         r.Header.Get("X-Org"),
			Project:     r.Header.Get("X-Project"),
			Environment: r.Header.Get("X-Environment"),
		}
		if !s.AuthZ.Allow(p, action, res) {
			http.Error(w, "forbidden", 403)
			return
		}
		next(w, r, p)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func stringsContains(s, sub string) bool {
	return strings.Contains(s, sub)
}

// Cluster is a registered cluster connection (no raw credentials).
type Cluster struct {
	Name           string    `json:"name"`
	Org            string    `json:"org,omitempty"`
	KubeconfigRef  string    `json:"kubeconfigRef,omitempty"` // external secret ref, never inline
	Server         string    `json:"server,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}
