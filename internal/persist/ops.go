package persist

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// JobView is a tenant-visible job status.
type JobView struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	RunID       string `json:"runId"`
	Org         string `json:"org"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
	MaxAttempts int    `json:"maxAttempts"`
	FenceToken  int64  `json:"fenceToken,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	OperationID string `json:"operationId,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
}

// GetJob returns a job if it belongs to org.
func (s *Store) GetJob(org, id string) (*JobView, error) {
	var j JobView
	var last, op, created, updated sql.NullString
	err := s.queryRow(`
SELECT id, kind, run_id, org, status, attempts, max_attempts, fence_token,
       COALESCE(last_error,''), COALESCE(operation_id,''), created_at, updated_at
FROM jobs WHERE id=? AND org=?
`, id, org).Scan(&j.ID, &j.Kind, &j.RunID, &j.Org, &j.Status, &j.Attempts, &j.MaxAttempts, &j.FenceToken,
		&last, &op, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.LastError = last.String
	j.OperationID = op.String
	j.CreatedAt = created.String
	j.UpdatedAt = updated.String
	return &j, nil
}

// ListJobs lists jobs for org (optional runID filter), newest first.
func (s *Store) ListJobs(org, runID string, limit int) ([]JobView, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if runID != "" {
		rows, err = s.query(`
SELECT id, kind, run_id, org, status, attempts, max_attempts, fence_token,
       COALESCE(last_error,''), COALESCE(operation_id,''), created_at, updated_at
FROM jobs WHERE org=? AND run_id=? ORDER BY created_at DESC LIMIT ?`, org, runID, limit)
	} else {
		rows, err = s.query(`
SELECT id, kind, run_id, org, status, attempts, max_attempts, fence_token,
       COALESCE(last_error,''), COALESCE(operation_id,''), created_at, updated_at
FROM jobs WHERE org=? ORDER BY created_at DESC LIMIT ?`, org, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobView
	for rows.Next() {
		var j JobView
		if err := rows.Scan(&j.ID, &j.Kind, &j.RunID, &j.Org, &j.Status, &j.Attempts, &j.MaxAttempts, &j.FenceToken,
			&j.LastError, &j.OperationID, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// RetryJob requeues a failed/cancelled job (new fence, pending).
func (s *Store) RetryJob(org, id string) error {
	res, err := s.exec(`UPDATE jobs SET status='pending', lease_holder=NULL, lease_until=NULL,
		last_error=NULL, attempts=0, updated_at=? WHERE id=? AND org=? AND status IN ('failed','cancelled','done')`,
		s.now(), id, org)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CancelJob cancels one job by id in org.
func (s *Store) CancelJob(org, id string) error {
	res, err := s.exec(`UPDATE jobs SET status='cancelled', lease_holder=NULL, lease_until=NULL,
		last_error='cancelled', updated_at=? WHERE id=? AND org=? AND status IN ('pending','leased')`,
		s.now(), id, org)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// AuditEntry is a durable audit row.
type AuditEntry struct {
	Time   string `json:"time"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
	Detail string `json:"detail,omitempty"`
	Org    string `json:"org"`
}

// ListAudit returns tenant-filtered audit log.
func (s *Store) ListAudit(org string, limit int) ([]AuditEntry, error) {
	if org == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.query(`SELECT time, actor, action, target, COALESCE(detail,''), COALESCE(org,'')
		FROM audit WHERE org=? ORDER BY time DESC LIMIT ?`, org, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.Time, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.Org); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PurgeOldRuns deletes runs older than retention (by updated_at) for org.
func (s *Store) PurgeOldRuns(org string, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339Nano)
	res, err := s.exec(`DELETE FROM runs WHERE org=? AND updated_at < ?`, org, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// BackupSQLite copies the SQLite database file to destPath (no-op for postgres).
func (s *Store) BackupSQLite(srcPath, destPath string) error {
	if s.dialect != "sqlite" {
		return fmt.Errorf("backup file copy only supported for sqlite (use pg_dump for postgres)")
	}
	if srcPath == "" || destPath == "" {
		return fmt.Errorf("src and dest required")
	}
	_ = os.MkdirAll(filepath.Dir(destPath), 0o755)
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// RestoreSQLite replaces dest DB file with backup (caller must not have open Store).
func RestoreSQLite(backupPath, destPath string) error {
	in, err := os.Open(backupPath)
	if err != nil {
		return err
	}
	defer in.Close()
	_ = os.MkdirAll(filepath.Dir(destPath), 0o755)
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
