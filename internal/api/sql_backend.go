package api

import (
	"context"
	"errors"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/persist"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

// SQLBackend adapts persist.Store to Backend.
type SQLBackend struct {
	S *persist.Store
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, persist.ErrConflict) {
		return ErrConflict
	}
	if errors.Is(err, persist.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func (b *SQLBackend) CreateRun(r *run.RehearsalRun) error { return mapErr(b.S.CreateRun(r)) }
func (b *SQLBackend) UpdateRun(r *run.RehearsalRun) error { return mapErr(b.S.UpdateRun(r)) }
func (b *SQLBackend) GetRun(org, id string) (*run.RehearsalRun, error) {
	return b.S.GetRun(org, id)
}
func (b *SQLBackend) GetRunByIdempotency(org, key string) (*run.RehearsalRun, error) {
	return b.S.GetRunByIdempotency(org, key)
}
func (b *SQLBackend) ListRuns(org string) ([]*run.RehearsalRun, error) { return b.S.ListRuns(org) }

func (b *SQLBackend) PutCluster(c Cluster) error {
	return b.S.PutCluster(persist.Cluster{
		Name: c.Name, Org: c.Org, KubeconfigRef: c.KubeconfigRef, Server: c.Server, CreatedAt: c.CreatedAt,
	})
}
func (b *SQLBackend) ListClusters(org string) ([]Cluster, error) {
	list, err := b.S.ListClusters(org)
	if err != nil {
		return nil, err
	}
	out := make([]Cluster, 0, len(list))
	for _, c := range list {
		out = append(out, Cluster{
			Name: c.Name, Org: c.Org, KubeconfigRef: c.KubeconfigRef, Server: c.Server, CreatedAt: c.CreatedAt,
		})
	}
	return out, nil
}
func (b *SQLBackend) PutPolicy(id, org string, p map[string]any) error {
	return b.S.PutPolicy(id, org, p)
}
func (b *SQLBackend) ListPolicies(org string) ([]map[string]any, error) {
	return b.S.ListPolicies(org)
}
func (b *SQLBackend) GetOrgPolicy(org string) (map[string]any, error) {
	return b.S.GetOrgPolicy(org)
}
func (b *SQLBackend) RecordCalibration(org string, o calibrate.Outcome) error {
	return b.S.RecordCalibration(org, o)
}
func (b *SQLBackend) CalibrationReport(org string) calibrate.Report {
	return b.S.CalibrationReport(org)
}
func (b *SQLBackend) Audit(actor, action, target, detail, org string) {
	_ = b.S.Audit(actor, action, target, detail, org)
}
func (b *SQLBackend) Enqueue(kind, runID, org, payload, operationID string) (string, error) {
	return b.S.Enqueue(kind, runID, org, payload, operationID)
}
func (b *SQLBackend) CancelJobsForRun(org, runID string) (int, error) {
	return b.S.CancelJobsForRun(org, runID)
}
func (b *SQLBackend) JobStats() (pending, leased, done, failed int) { return b.S.JobStats() }
func (b *SQLBackend) Ready() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := b.S.Ping(ctx); err != nil {
		return err
	}
	if b.S.Blob != nil {
		return b.S.Blob.Ready()
	}
	return nil
}
func (b *SQLBackend) GetJob(org, id string) (*persist.JobView, error) { return b.S.GetJob(org, id) }
func (b *SQLBackend) ListJobs(org, runID string, limit int) ([]persist.JobView, error) {
	return b.S.ListJobs(org, runID, limit)
}
func (b *SQLBackend) CancelJob(org, id string) error { return mapErr(b.S.CancelJob(org, id)) }
func (b *SQLBackend) RetryJob(org, id string) error  { return mapErr(b.S.RetryJob(org, id)) }
func (b *SQLBackend) ListAudit(org string, limit int) ([]persist.AuditEntry, error) {
	return b.S.ListAudit(org, limit)
}
func (b *SQLBackend) SchemaVersion() int { return b.S.SchemaVersion() }
