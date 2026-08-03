package api

import (
	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

// Backend is the control-plane data plane (memory or SQL).
type Backend interface {
	PutRun(r *run.RehearsalRun) error
	GetRun(id string) (*run.RehearsalRun, error)
	GetRunByIdempotency(key string) (*run.RehearsalRun, error)
	ListRuns(org string) ([]*run.RehearsalRun, error)
	PutCluster(c Cluster) error
	ListClusters(org string) ([]Cluster, error)
	PutPolicy(id, org string, p map[string]any) error
	ListPolicies(org string) ([]map[string]any, error)
	GetOrgPolicy(org string) (map[string]any, error)
	RecordCalibration(o calibrate.Outcome) error
	CalibrationReport() calibrate.Report
	Audit(actor, action, target, detail string)
	// Enqueue returns job id for async work; empty implementation returns "" and nil for sync-only backends.
	Enqueue(kind, runID, org, payload string) (string, error)
	JobStats() (pending, leased, done, failed int)
}
