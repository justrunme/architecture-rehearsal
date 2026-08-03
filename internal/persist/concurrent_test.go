package persist_test

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/persist"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

func TestConcurrentWorkersClaimOnce(t *testing.T) {
	dir := t.TempDir()
	s, err := persist.Open(filepath.Join(dir, "c.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rr := run.NewRun("r", "", run.Spec{})
	rr.Labels = map[string]string{"org": "o"}
	if err := s.CreateRun(rr); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(persist.JobAdvanceRun, "r", "o", `{"action":"cancel"}`, "op-once"); err != nil {
		t.Fatal(err)
	}

	var claimed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			job, err := s.ClaimJob(context.Background(), "w", time.Minute)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if job != nil {
				claimed.Add(1)
				_ = s.CompleteJob(job.ID, job.FenceToken)
			}
		}(i)
	}
	wg.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("want exactly 1 claim, got %d", claimed.Load())
	}
}

func TestBackupRestoreSQLite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "main.db")
	s, err := persist.Open(dbPath, "")
	if err != nil {
		t.Fatal(err)
	}
	rr := run.NewRun("bak", "", run.Spec{})
	rr.Labels = map[string]string{"org": "o"}
	if err := s.CreateRun(rr); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	bak := filepath.Join(dir, "backup.db")
	// open again for BackupSQLite dialect check
	s2, err := persist.Open(dbPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.BackupSQLite(dbPath, bak); err != nil {
		t.Fatal(err)
	}
	_ = s2.Close()

	restored := filepath.Join(dir, "restored.db")
	if err := persist.RestoreSQLite(bak, restored); err != nil {
		t.Fatal(err)
	}
	s3, err := persist.Open(restored, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	got, err := s3.GetRun("o", "bak")
	if err != nil || got == nil {
		t.Fatalf("restored run missing: %v", err)
	}
}
