package api

import (
	"errors"

	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

// ErrConflict and ErrNotFound mirror persist for API handlers.
var (
	ErrConflict = errors.New("conflict")
	ErrNotFound = errors.New("not found")
)

// Backend is the control-plane data plane (memory or SQL).
// All run access is tenant-scoped by org.
type Backend interface {
	// CreateRun inserts; returns ErrConflict if (org,id) exists.
	CreateRun(r *run.RehearsalRun) error
	// UpdateRun updates existing run in same org.
	UpdateRun(r *run.RehearsalRun) error
	GetRun(org, id string) (*run.RehearsalRun, error)
	GetRunByIdempotency(org, key string) (*run.RehearsalRun, error)
	ListRuns(org string) ([]*run.RehearsalRun, error)
	PutCluster(c Cluster) error
	ListClusters(org string) ([]Cluster, error)
	PutPolicy(id, org string, p map[string]any) error
	ListPolicies(org string) ([]map[string]any, error)
	GetOrgPolicy(org string) (map[string]any, error)
	RecordCalibration(org string, o calibrate.Outcome) error
	CalibrationReport(org string) calibrate.Report
	Audit(actor, action, target, detail, org string)
	// Enqueue returns job id; operationID enables exactly-once logical enqueue.
	Enqueue(kind, runID, org, payload, operationID string) (string, error)
	CancelJobsForRun(org, runID string) (int, error)
	JobStats() (pending, leased, done, failed int)
	// Ready reports backend health (nil = ok).
	Ready() error
}
