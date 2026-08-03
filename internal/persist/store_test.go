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

func TestSQLiteOpenPutGetClaim(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	blobRoot := filepath.Join(dir, "blobs")
	s, err := persist.Open(dbPath, blobRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rr := run.NewRun("r1", "ik-1", run.Spec{BaselineRef: "b.json", ChangeRef: "c.json"})
	rr.Labels = map[string]string{"org": "acme", "project": "p1"}
	if err := s.PutRun(rr); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun("r1")
	if err != nil || got == nil {
		t.Fatalf("get: %v %#v", err, got)
	}
	if got.Labels["org"] != "acme" {
		t.Fatalf("org %q", got.Labels["org"])
	}
	byKey, err := s.GetRunByIdempotency("ik-1")
	if err != nil || byKey == nil || byKey.ID != "r1" {
		t.Fatalf("idempotency: %v %#v", err, byKey)
	}

	// upsert
	rr.Status.Phase = run.PhaseCollecting
	if err := s.PutRun(rr); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetRun("r1")
	if got.Status.Phase != run.PhaseCollecting {
		t.Fatalf("phase %s", got.Status.Phase)
	}

	list, err := s.ListRuns("acme")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v n=%d", err, len(list))
	}

	// cluster + policy
	if err := s.PutCluster(persist.Cluster{Name: "prod", Org: "acme", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	cls, _ := s.ListClusters("acme")
	if len(cls) != 1 {
		t.Fatalf("clusters %d", len(cls))
	}
	if err := s.PutPolicy("p1", "acme", map[string]any{"block": []any{`risk == "critical"`}}); err != nil {
		t.Fatal(err)
	}
	pol, err := s.GetOrgPolicy("acme")
	if err != nil || pol == nil {
		t.Fatalf("policy %v", err)
	}

	// blob
	d, path, err := s.Blob.Put([]byte(`{"chain":true}`), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if len(d) != 64 {
		t.Fatalf("digest len %d", len(d))
	}
	raw, err := s.Blob.Get(d)
	if err != nil || string(raw) != `{"chain":true}` {
		t.Fatalf("blob get %v %s path=%s", err, raw, path)
	}

	// jobs
	jid, err := s.Enqueue(persist.JobAdvanceRun, "r1", "acme", `{"workDir":"."}`)
	if err != nil || jid == "" {
		t.Fatalf("enqueue %v %s", err, jid)
	}
	p, l, d0, f := s.JobStats()
	if p != 1 || l != 0 || d0 != 0 || f != 0 {
		t.Fatalf("stats pending=%d leased=%d done=%d failed=%d", p, l, d0, f)
	}
	job, err := s.ClaimJob(context.Background(), "w1", time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim %v %#v", err, job)
	}
	if job.Kind != persist.JobAdvanceRun || job.RunID != "r1" {
		t.Fatalf("%+v", job)
	}
	// second claim empty
	job2, err := s.ClaimJob(context.Background(), "w2", time.Minute)
	if err != nil || job2 != nil {
		t.Fatalf("expected nil claim, got %#v err=%v", job2, err)
	}
	if err := s.CompleteJob(job.ID); err != nil {
		t.Fatal(err)
	}
	_, _, done, _ := s.JobStats()
	if done != 1 {
		t.Fatalf("done=%d", done)
	}

	_ = s.RecordCalibration(calibrate.Outcome{Scenario: "rwo", Predicted: true, Observed: true, Verified: true})
	rep := s.CalibrationReport()
	if len(rep.Scenarios) == 0 {
		t.Fatalf("calibration empty: %+v", rep)
	}
	_ = s.Audit("actor", "test", "r1", "ok")
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
	if err := s.PutRun(rr); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(persist.JobAdvanceRun, "run-cancel", "o", `{"action":"cancel"}`); err != nil {
		t.Fatal(err)
	}
	w := &persist.Worker{Store: s, Holder: "t", WorkDir: dir, Interval: 30 * time.Millisecond, LeaseTTL: time.Minute}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go w.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.GetRun("run-cancel")
		if got != nil && got.Status.Phase == run.PhaseCancelled {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	got, _ := s.GetRun("run-cancel")
	t.Fatalf("want Cancelled, got %v", got.Status.Phase)
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
