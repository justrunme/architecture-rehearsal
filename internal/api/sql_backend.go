package api

import (
	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/persist"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

// SQLBackend adapts persist.Store to Backend.
type SQLBackend struct {
	S *persist.Store
}

func (b *SQLBackend) PutRun(r *run.RehearsalRun) error { return b.S.PutRun(r) }
func (b *SQLBackend) GetRun(id string) (*run.RehearsalRun, error) {
	return b.S.GetRun(id)
}
func (b *SQLBackend) GetRunByIdempotency(key string) (*run.RehearsalRun, error) {
	return b.S.GetRunByIdempotency(key)
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
func (b *SQLBackend) RecordCalibration(o calibrate.Outcome) error {
	return b.S.RecordCalibration(o)
}
func (b *SQLBackend) CalibrationReport() calibrate.Report { return b.S.CalibrationReport() }
func (b *SQLBackend) Audit(actor, action, target, detail string) {
	_ = b.S.Audit(actor, action, target, detail)
}
func (b *SQLBackend) Enqueue(kind, runID, org, payload string) (string, error) {
	return b.S.Enqueue(kind, runID, org, payload)
}
func (b *SQLBackend) JobStats() (pending, leased, done, failed int) { return b.S.JobStats() }
