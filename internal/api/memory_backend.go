package api

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

// MemoryBackend is the in-process backend (tests / --memory).
type MemoryBackend struct {
	mu       sync.RWMutex
	runs     map[string]*run.RehearsalRun
	idem     map[string]string
	clusters map[string]Cluster // key org/name
	policies map[string]map[string]any
	audit    []AuditEntry
	cal      *calibrate.Store
	jobs     []memJob
}

type memJob struct {
	ID, Kind, RunID, Org, Payload, Status string
}

// AuditEntry is an API audit record.
type AuditEntry struct {
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail,omitempty"`
}

// NewMemoryBackend creates an empty backend.
func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{
		runs:     map[string]*run.RehearsalRun{},
		idem:     map[string]string{},
		clusters: map[string]Cluster{},
		policies: map[string]map[string]any{},
		cal:      calibrate.NewStore(),
	}
}

func (s *MemoryBackend) PutRun(r *run.RehearsalRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *r
	s.runs[r.ID] = &cp
	if r.IdempotencyKey != "" {
		s.idem[r.IdempotencyKey] = r.ID
	}
	return nil
}

func (s *MemoryBackend) GetRun(id string) (*run.RehearsalRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.runs[id]
	if r == nil {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (s *MemoryBackend) GetRunByIdempotency(key string) (*run.RehearsalRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id := s.idem[key]
	if id == "" {
		return nil, nil
	}
	r := s.runs[id]
	if r == nil {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (s *MemoryBackend) ListRuns(org string) ([]*run.RehearsalRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*run.RehearsalRun
	for _, r := range s.runs {
		if org != "" && r.Labels["org"] != org {
			continue
		}
		cp := *r
		out = append(out, &cp)
	}
	return out, nil
}

func (s *MemoryBackend) PutCluster(c Cluster) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clusters[c.Org+"/"+c.Name] = c
	return nil
}

func (s *MemoryBackend) ListClusters(org string) ([]Cluster, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Cluster
	for _, c := range s.clusters {
		if org != "" && c.Org != org {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *MemoryBackend) PutPolicy(id, org string, p map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p["id"] = id
	p["org"] = org
	s.policies[org+"/"+id] = p
	return nil
}

func (s *MemoryBackend) ListPolicies(org string) ([]map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []map[string]any
	for _, p := range s.policies {
		if o, _ := p["org"].(string); org != "" && o != org {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *MemoryBackend) GetOrgPolicy(org string) (map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.policies {
		if o, _ := p["org"].(string); o == org {
			return p, nil
		}
	}
	return nil, nil
}

func (s *MemoryBackend) RecordCalibration(o calibrate.Outcome) error {
	s.cal.Record(o)
	return nil
}

func (s *MemoryBackend) CalibrationReport() calibrate.Report {
	return s.cal.Report()
}

func (s *MemoryBackend) Audit(actor, action, target, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, AuditEntry{
		Time: time.Now().UTC(), Actor: actor, Action: action, Target: target, Detail: detail,
	})
}

func (s *MemoryBackend) Enqueue(kind, runID, org, payload string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := "mem-job-" + runID
	s.jobs = append(s.jobs, memJob{ID: id, Kind: kind, RunID: runID, Org: org, Payload: payload, Status: "pending"})
	return id, nil
}

func (s *MemoryBackend) JobStats() (pending, leased, done, failed int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, j := range s.jobs {
		switch j.Status {
		case "pending":
			pending++
		case "leased":
			leased++
		case "done":
			done++
		case "failed":
			failed++
		}
	}
	return
}

// DrainJob pops one pending job for in-process sync fallback tests.
func (s *MemoryBackend) DrainJob() (kind, runID, org, payload string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, j := range s.jobs {
		if j.Status == "pending" {
			s.jobs[i].Status = "done"
			return j.Kind, j.RunID, j.Org, j.Payload, true
		}
	}
	return "", "", "", "", false
}

// Ensure json used
var _ = json.Marshal
