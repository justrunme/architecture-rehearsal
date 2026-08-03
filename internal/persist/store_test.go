package persist_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/persist"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

func TestSQLiteTenantIdentity(t *testing.T) {
	dir := t.TempDir()
	s, err := persist.Open(filepath.Join(dir, "test.db"), filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rr := run.NewRun("r1", "ik-1", run.Spec{BaselineRef: "b.json", ChangeRef: "c.json"})
	rr.Labels = map[string]string{"org": "acme", "project": "p1"}
	if err := s.CreateRun(rr); err != nil {
		t.Fatal(err)
	}
	// same id other org OK
	rr2 := run.NewRun("r1", "ik-other", run.Spec{})
	rr2.Labels = map[string]string{"org": "other"}
	if err := s.CreateRun(rr2); err != nil {
		t.Fatal(err)
	}
	// same org same id conflict
	if err := s.CreateRun(rr); err != persist.ErrConflict {
		t.Fatalf("want conflict got %v", err)
	}
	// attacker cannot get victim run by id without org
	got, err := s.GetRun("other", "r1")
	if err != nil || got == nil || got.Labels["org"] != "other" {
		t.Fatalf("other org run: %v %#v", err, got)
	}
	got, err = s.GetRun("acme", "r1")
	if err != nil || got == nil || got.Labels["org"] != "acme" {
		t.Fatalf("acme run: %v %#v", err, got)
	}
	// idempotency org-scoped: same key different orgs OK
	byKey, err := s.GetRunByIdempotency("acme", "ik-1")
	if err != nil || byKey == nil || byKey.ID != "r1" {
		t.Fatalf("idempotency: %v %#v", err, byKey)
	}
	// other org cannot resolve acme's key
	byKey, err = s.GetRunByIdempotency("other", "ik-1")
	if err != nil || byKey != nil {
		t.Fatalf("cross-org idem lookup must miss: %v %#v", err, byKey)
	}

	rr.Status.Phase = run.PhaseCollecting
	if err := s.UpdateRun(rr); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetRun("acme", "r1")
	if got.Status.Phase != run.PhaseCollecting {
		t.Fatalf("phase %s", got.Status.Phase)
	}

	list, err := s.ListRuns("acme")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v n=%d", err, len(list))
	}

	if err := s.PutCluster(persist.Cluster{Name: "prod", Org: "acme", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutPolicy("p1", "acme", map[string]any{"block": []any{`risk == "critical"`}}); err != nil {
		t.Fatal(err)
	}

	d, path, err := s.Blob.Put([]byte(`{"chain":true}`), "application/json")
	if err != nil || len(d) != 64 {
		t.Fatalf("blob %v %s path=%s", err, d, path)
	}

	jid, err := s.Enqueue(persist.JobAdvanceRun, "r1", "acme", `{"workDir":"."}`, "")
	if err != nil || jid == "" {
		t.Fatalf("enqueue %v %s", err, jid)
	}
	// exactly-once operation id
	jid2, err := s.Enqueue(persist.JobAdvanceRun, "r1", "acme", `{}`, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	jid3, err := s.Enqueue(persist.JobAdvanceRun, "r1", "acme", `{}`, "op-1")
	if err != nil || jid2 != jid3 {
		t.Fatalf("op id dedupe %s vs %s err=%v", jid2, jid3, err)
	}

	job, err := s.ClaimJob(context.Background(), "w1", time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim %v %#v", err, job)
	}
	// fence: wrong fence fails complete
	if err := s.CompleteJob(job.ID, job.FenceToken+99); err != persist.ErrStaleFence {
		t.Fatalf("want stale fence got %v", err)
	}
	if err := s.CompleteJob(job.ID, job.FenceToken); err != nil {
		t.Fatal(err)
	}

	// calibration org scoped
	_ = s.RecordCalibration("acme", calibrate.Outcome{Scenario: "rwo", Predicted: true, Observed: true, Verified: true})
	_ = s.RecordCalibration("other", calibrate.Outcome{Scenario: "rwo", Predicted: true, Observed: false, Verified: true})
	repA := s.CalibrationReport("acme")
	repB := s.CalibrationReport("other")
	if len(repA.Scenarios) == 0 || len(repB.Scenarios) == 0 {
		t.Fatalf("cal empty a=%+v b=%+v", repA, repB)
	}
	// cancel jobs
	_, _ = s.Enqueue(persist.JobAdvanceRun, "r1", "acme", `{}`, "cancel-me")
	n, err := s.CancelJobsForRun("acme", "r1")
	if err != nil || n < 1 {
		t.Fatalf("cancel n=%d err=%v", n, err)
	}
	_ = s.Audit("actor", "test", "r1", "ok", "acme")
}

func TestWorkerAdvanceCancel(t *testing.T) {
	dir := t.TempDir()
	s, err := persist.Open(filepath.Join(dir, "w.db"), filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rr := run.NewRun("run-cancel", "", run.Spec{BaselineRef: "b", ChangeRef: "c"})
	rr.Labels = map[string]string{"org": "o"}
	if err := s.CreateRun(rr); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(persist.JobAdvanceRun, "run-cancel", "o", `{"action":"cancel"}`, ""); err != nil {
		t.Fatal(err)
	}
	w := &persist.Worker{Store: s, Holder: "t", WorkDir: dir, Interval: 30 * time.Millisecond, LeaseTTL: time.Minute}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go w.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.GetRun("o", "run-cancel")
		if got != nil && got.Status.Phase == run.PhaseCancelled {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	got, _ := s.GetRun("o", "run-cancel")
	t.Fatalf("want Cancelled, got %v", got.Status.Phase)
}

func TestLeaseRenewAndFence(t *testing.T) {
	dir := t.TempDir()
	s, err := persist.Open(filepath.Join(dir, "lease.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rr := run.NewRun("r", "", run.Spec{})
	rr.Labels = map[string]string{"org": "o"}
	_ = s.CreateRun(rr)
	_, _ = s.Enqueue(persist.JobAdvanceRun, "r", "o", `{}`, "")
	job, err := s.ClaimJob(context.Background(), "w1", 2*time.Second)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err := s.RenewLease(job.ID, "w1", job.FenceToken, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.RenewLease(job.ID, "w2", job.FenceToken, time.Minute); err != persist.ErrStaleFence {
		// wrong holder
		if err == nil {
			t.Fatal("wrong holder must fail")
		}
	}
	// expire lease and reclaim
	_, _ = s.DB().Exec(`UPDATE jobs SET lease_until=? WHERE id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), job.ID)
	job2, err := s.ClaimJob(context.Background(), "w2", time.Minute)
	if err != nil || job2 == nil {
		t.Fatalf("reclaim %v %#v", err, job2)
	}
	if job2.FenceToken <= job.FenceToken {
		t.Fatalf("fence must increase %d -> %d", job.FenceToken, job2.FenceToken)
	}
	// old fence cannot complete
	if err := s.CompleteJob(job.ID, job.FenceToken); err != persist.ErrStaleFence {
		t.Fatalf("old fence complete: %v", err)
	}
	if err := s.CompleteJob(job2.ID, job2.FenceToken); err != nil {
		t.Fatal(err)
	}
}

func TestBlobIdempotentPut(t *testing.T) {
	dir := t.TempDir()
	b, err := persist.NewBlobStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	d1, _, err := b.Put([]byte("hello"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	d2, _, err := b.Put([]byte("hello"), "text/plain")
	if err != nil || d1 != d2 {
		t.Fatalf("%s vs %s err=%v", d1, d2, err)
	}
}
