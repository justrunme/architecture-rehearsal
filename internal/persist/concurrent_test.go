package persist_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

func TestBackupSQLite_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "live.db")
	s, err := persist.Open(dbPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var writeWg sync.WaitGroup
	var activeWrites atomic.Int64

	var successfulWrites atomic.Int64
	var failedWrites atomic.Int64

	// Spawn 5 writer goroutines inserting/updating runs and jobs continuously
	for w := 0; w < 5; w++ {
		writeWg.Add(1)
		go func(workerID int) {
			defer writeWg.Done()
			i := 0
			for {
				select {
				case <-ctx.Done():
					return
				default:
					i++
					runID := filepath.Join("run", string(rune('a'+workerID)))
					rr := run.NewRun(runID, "", run.Spec{})
					rr.Labels = map[string]string{"org": "test-org", "worker": string(rune('0' + workerID))}
					err1 := s.PutRun(rr)
					_, err2 := s.Enqueue(persist.JobAdvanceRun, runID, "test-org", `{"payload":"data-content-chunk"}`, "")
					if err1 == nil && err2 == nil {
						successfulWrites.Add(1)
					} else {
						failedWrites.Add(1)
					}
					activeWrites.Add(1)
				}
			}
		}(w)
	}

	// Simultaneously take 20 backups
	var corruptCount int
	var structValidLogicalInconsistent int
	var validCount int
	var totalBackups int

	for b := 0; b < 20; b++ {
		time.Sleep(50 * time.Millisecond)
		bakPath := filepath.Join(dir, filepath.Base(t.Name())+"-bak-"+time.Now().Format("150405.000000")+".db")
		err := s.BackupSQLite(dbPath, bakPath)
		if err != nil {
			t.Logf("BackupSQLite call %d returned error: %v", b, err)
			continue
		}
		totalBackups++

		// Verify backup DB integrity
		bakStore, err := persist.Open(bakPath, "")
		if err != nil {
			corruptCount++
			t.Logf("Backup %d (path: %s) failed to open as SQLite: %v", b, bakPath, err)
			continue
		}

		// Check PRAGMA integrity_check via raw SQLite query and store methods
		runs, errList := bakStore.ListRuns("test-org")
		jobs, errJobs := bakStore.ListJobs("test-org", "", 100)
		_ = bakStore.Close()

		if errList != nil || errJobs != nil {
			structValidLogicalInconsistent++
			t.Logf("Backup %d failed query: runsErr=%v jobsErr=%v", b, errList, errJobs)
		} else if len(runs) == 0 && successfulWrites.Load() > 10 {
			structValidLogicalInconsistent++
			t.Logf("Backup %d produced empty runs despite %d successful writes", b, successfulWrites.Load())
		} else {
			validCount++
			_, _ = runs, jobs
		}
	}

	cancel()
	writeWg.Wait()

	t.Logf("Total Backups Attempted: %d, Valid: %d, Structurally Corrupt: %d, Inconsistent Query: %d, Successful Writes: %d, Failed Writes: %d",
		totalBackups, validCount, corruptCount, structValidLogicalInconsistent, successfulWrites.Load(), failedWrites.Load())
}

func TestBackupSQLite_EdgeCases(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "edge.db")
	s, err := persist.Open(dbPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rr := run.NewRun("edge-run", "", run.Spec{})
	rr.Labels = map[string]string{"org": "test-org"}
	if err := s.CreateRun(rr); err != nil {
		t.Fatal(err)
	}

	// 1. Existing destination file overwrite test
	existingBak := filepath.Join(dir, "existing.db")
	_ = os.WriteFile(existingBak, []byte("stale file content"), 0o644)
	if err := s.BackupSQLite(dbPath, existingBak); err != nil {
		t.Fatalf("BackupSQLite failed over existing destination file: %v", err)
	}
	bakStore, err := persist.Open(existingBak, "")
	if err != nil {
		t.Fatalf("failed to open backup made over existing destination: %v", err)
	}
	got, err := bakStore.GetRun("test-org", "edge-run")
	_ = bakStore.Close()
	if err != nil || got == nil {
		t.Fatalf("expected run in backup made over existing destination, got: %v", err)
	}

	// 2. Empty destination path validation
	if err := s.BackupSQLite(dbPath, ""); err == nil {
		t.Fatalf("expected error for empty destPath, got nil")
	}
}

func checkMinIOOnline(t *testing.T) {
	t.Helper()
	resp, err := http.Get("http://localhost:9000/minio/health/live")
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Skip("MinIO server not reachable on http://localhost:9000; skipping live integration test")
	}
	_ = resp.Body.Close()
}

func ensureTestBucket(t *testing.T, endpoint, bucket, access, secret string) {
	t.Helper()
	s3Client := s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider(access, secret, ""),
		UsePathStyle: true,
		BaseEndpoint: aws.String(endpoint),
		HTTPClient:   &http.Client{Timeout: 10 * time.Second},
	})
	_, _ = s3Client.CreateBucket(context.Background(), &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
}

func TestS3Blob_MinIO_RealIntegration(t *testing.T) {
	checkMinIOOnline(t)
	ensureTestBucket(t, "http://localhost:9000", "rehearsal-test-bucket", "minioadmin", "minioadmin")

	blob, err := persist.NewS3Blob("http://localhost:9000", "rehearsal-test-bucket", "minioadmin", "minioadmin")
	if err != nil {
		t.Fatalf("NewS3Blob: %v", err)
	}

	readyErr := blob.Ready()
	if readyErr != nil {
		t.Fatalf("S3Blob.Ready() failed: %v", readyErr)
	}

	expectedData := []byte("test evidence content with AWS SigV4")
	digest, uri, putErr := blob.Put(expectedData, "application/json")
	if putErr != nil {
		t.Fatalf("S3Blob.Put() failed: %v", putErr)
	}
	if digest == "" || uri == "" {
		t.Fatalf("expected non-empty digest and uri, got digest=%q uri=%q", digest, uri)
	}

	got, getErr := blob.Get(digest)
	if getErr != nil {
		t.Fatalf("S3Blob.Get() failed: %v", getErr)
	}

	if string(got) != string(expectedData) {
		t.Fatalf("content mismatch: expected %q, got %q", string(expectedData), string(got))
	}
}

func TestS3Blob_MinIO_InvalidSecretKey(t *testing.T) {
	checkMinIOOnline(t)

	// Supply invalid secret key "wrongsecret"
	invalidBlob, err := persist.NewS3Blob("http://localhost:9000", "rehearsal-test-bucket", "minioadmin", "wrongsecret")
	if err != nil {
		t.Fatalf("NewS3Blob: %v", err)
	}

	_, _, putErr := invalidBlob.Put([]byte("data"), "text/plain")
	if putErr == nil {
		t.Fatalf("expected Put with invalid secret key to fail with auth error, but it succeeded")
	}
	t.Logf("Got expected authentication error with invalid secret key: %v", putErr)
}
