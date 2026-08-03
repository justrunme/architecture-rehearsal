package api

import (
	"errors"

	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/persist"
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
	CreateRun(r *run.RehearsalRun) error
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
	Enqueue(kind, runID, org, payload, operationID string) (string, error)
	CancelJobsForRun(org, runID string) (int, error)
	JobStats() (pending, leased, done, failed int)
	Ready() error

	// v1.3 ops surface
	GetJob(org, id string) (*persist.JobView, error)
	ListJobs(org, runID string, limit int) ([]persist.JobView, error)
	CancelJob(org, id string) error
	RetryJob(org, id string) error
	ListAudit(org string, limit int) ([]persist.AuditEntry, error)
	SchemaVersion() int
}
