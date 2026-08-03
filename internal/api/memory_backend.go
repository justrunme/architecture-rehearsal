package api

import (
	"fmt"
	"sync"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

// MemoryBackend is the in-process backend (tests / --memory).
type MemoryBackend struct {
	mu       sync.RWMutex
	runs     map[string]*run.RehearsalRun // key: org+"\x00"+id
	idem     map[string]string           // key: org+"\x00"+key → run id
	clusters map[string]Cluster
	policies map[string]map[string]any
	audit    []AuditEntry
	cal      map[string]*calibrate.Store // per org
	jobs     []memJob
}

type memJob struct {
	ID, Kind, RunID, Org, Payload, Status, OpID string
}

// AuditEntry is an API audit record.
type AuditEntry struct {
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail,omitempty"`
	Org    string    `json:"org,omitempty"`
}

// NewMemoryBackend creates an empty backend.
func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{
		runs:     map[string]*run.RehearsalRun{},
		idem:     map[string]string{},
		clusters: map[string]Cluster{},
		policies: map[string]map[string]any{},
		cal:      map[string]*calibrate.Store{},
	}
}

func runKey(org, id string) string { return org + "\x00" + id }
func idemKey(org, key string) string { return org + "\x00" + key }

func (s *MemoryBackend) CreateRun(r *run.RehearsalRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	org := ""
	if r.Labels != nil {
		org = r.Labels["org"]
	}
	if org == "" || r.ID == "" {
		return fmt.Errorf("org and id required")
	}
	k := runKey(org, r.ID)
	if _, ok := s.runs[k]; ok {
		return ErrConflict
	}
	if r.IdempotencyKey != "" {
		ik := idemKey(org, r.IdempotencyKey)
		if _, ok := s.idem[ik]; ok {
			return ErrConflict
		}
		s.idem[ik] = r.ID
	}
	cp := *r
	s.runs[k] = &cp
	return nil
}

func (s *MemoryBackend) UpdateRun(r *run.RehearsalRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	org := ""
	if r.Labels != nil {
		org = r.Labels["org"]
	}
	k := runKey(org, r.ID)
	if _, ok := s.runs[k]; !ok {
		return ErrNotFound
	}
	cp := *r
	s.runs[k] = &cp
	return nil
}

func (s *MemoryBackend) GetRun(org, id string) (*run.RehearsalRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.runs[runKey(org, id)]
	if r == nil {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (s *MemoryBackend) GetRunByIdempotency(org, key string) (*run.RehearsalRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id := s.idem[idemKey(org, key)]
	if id == "" {
		return nil, nil
	}
	r := s.runs[runKey(org, id)]
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
		if r.Labels["org"] != org {
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
		if c.Org != org {
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
		if o, _ := p["org"].(string); o == org {
			out = append(out, p)
		}
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

func (s *MemoryBackend) RecordCalibration(org string, o calibrate.Outcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cal[org] == nil {
		s.cal[org] = calibrate.NewStore()
	}
	s.cal[org].Record(o)
	return nil
}

func (s *MemoryBackend) CalibrationReport(org string) calibrate.Report {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cal[org] == nil {
		return calibrate.NewStore().Report()
	}
	return s.cal[org].Report()
}

func (s *MemoryBackend) Audit(actor, action, target, detail, org string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, AuditEntry{
		Time: time.Now().UTC(), Actor: actor, Action: action, Target: target, Detail: detail, Org: org,
	})
}

func (s *MemoryBackend) Enqueue(kind, runID, org, payload, operationID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if operationID != "" {
		for _, j := range s.jobs {
			if j.Org == org && j.OpID == operationID {
				return j.ID, nil
			}
		}
	}
	id := fmt.Sprintf("mem-job-%d", time.Now().UnixNano())
	s.jobs = append(s.jobs, memJob{ID: id, Kind: kind, RunID: runID, Org: org, Payload: payload, Status: "pending", OpID: operationID})
	return id, nil
}

func (s *MemoryBackend) CancelJobsForRun(org, runID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for i, j := range s.jobs {
		if j.Org == org && j.RunID == runID && (j.Status == "pending" || j.Status == "leased") {
			s.jobs[i].Status = "cancelled"
			n++
		}
	}
	return n, nil
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

func (s *MemoryBackend) Ready() error { return nil }
