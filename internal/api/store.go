package api

import (
	"sync"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/run"
)

// MemoryStore is the default durable-enough in-process store (v0.11).
// Production deployments should swap for PostgreSQL + object storage.
type MemoryStore struct {
	mu       sync.RWMutex
	runs     map[string]*run.RehearsalRun
	idem     map[string]string // key → run id
	clusters map[string]Cluster
	policies map[string]map[string]any
	audit    []AuditEntry
}

// AuditEntry is an API audit record.
type AuditEntry struct {
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail,omitempty"`
}

// NewMemoryStore creates an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		runs:     map[string]*run.RehearsalRun{},
		idem:     map[string]string{},
		clusters: map[string]Cluster{},
		policies: map[string]map[string]any{},
	}
}

func (s *MemoryStore) Put(r *run.RehearsalRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// deep-ish copy via pointer store — caller owns mutation carefully
	cp := *r
	s.runs[r.ID] = &cp
	if r.IdempotencyKey != "" {
		s.idem[r.IdempotencyKey] = r.ID
	}
}

func (s *MemoryStore) Get(id string) *run.RehearsalRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.runs[id]
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}

func (s *MemoryStore) GetByIdempotency(key string) *run.RehearsalRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id := s.idem[key]
	if id == "" {
		return nil
	}
	r := s.runs[id]
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}

func (s *MemoryStore) ListRuns() []*run.RehearsalRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*run.RehearsalRun, 0, len(s.runs))
	for _, r := range s.runs {
		cp := *r
		out = append(out, &cp)
	}
	return out
}

func (s *MemoryStore) PutCluster(c Cluster) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clusters[c.Name] = c
}

func (s *MemoryStore) ListClusters() []Cluster {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Cluster, 0, len(s.clusters))
	for _, c := range s.clusters {
		out = append(out, c)
	}
	return out
}

func (s *MemoryStore) PutPolicy(id string, p map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policies[id] = p
}

func (s *MemoryStore) Audit(actor, action, target, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, AuditEntry{
		Time: time.Now().UTC(), Actor: actor, Action: action, Target: target, Detail: detail,
	})
}
