// Package persist provides durable storage for the control plane (v1.2).
// Default: SQLite (modernc). Optional: PostgreSQL via REHEARSAL_DATABASE_URL=postgres://...
package persist

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/run"
	_ "modernc.org/sqlite"
)

// Cluster is stored cluster connection (duplicated in api for JSON API surface).
type Cluster struct {
	Name          string    `json:"name"`
	Org           string    `json:"org,omitempty"`
	KubeconfigRef string    `json:"kubeconfigRef,omitempty"`
	Server        string    `json:"server,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

// Store is the durable control-plane repository.
type Store struct {
	db   *sql.DB
	Blob *BlobStore
	// dialect: sqlite|postgres
	dialect string
}

// Open opens a database.
// dsn:
//   - empty or file path → sqlite (file:path or path)
//   - postgres:// or postgresql:// → pgx stdlib
func Open(dsn string, blobRoot string) (*Store, error) {
	dialect := "sqlite"
	driver := "sqlite"
	openDSN := dsn
	if dsn == "" {
		openDSN = "file:rehearsal.db?_pragma=busy_timeout(5000)"
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		dialect = "postgres"
		driver = "pgx"
		// register pgx if available
		if err := registerPGX(); err != nil {
			return nil, err
		}
		openDSN = dsn
	} else if !strings.HasPrefix(dsn, "file:") && dsn != "" && !strings.Contains(dsn, "://") {
		// plain path
		if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil && filepath.Dir(dsn) != "." {
			// ignore if dir is .
		}
		openDSN = "file:" + dsn + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	}

	db, err := sql.Open(driver, openDSN)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	s := &Store{db: db, dialect: dialect}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if blobRoot != "" {
		bs, err := NewBlobStore(blobRoot)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		s.Blob = bs
	}
	return s, nil
}

func (s *Store) migrate() error {
	sqlText := schemaSQL
	if s.dialect == "postgres" {
		sqlText = strings.ReplaceAll(sqlText, "INTEGER PRIMARY KEY AUTOINCREMENT", "BIGSERIAL PRIMARY KEY")
		// postgres: unique on non-null only via partial index (NULLS DISTINCT in PG15+)
		sqlText = strings.ReplaceAll(sqlText,
			"CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_idem ON runs(idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key != '';",
			"CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_idem ON runs(idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> '';")
	}
	// multi-statement: exec each non-empty statement
	for _, stmt := range strings.Split(sqlText, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w\nSQL: %s", err, stmt)
		}
	}
	return nil
}

// rebind converts ? placeholders to $1,$2,... for PostgreSQL.
func (s *Store) rebind(q string) string {
	if s.dialect != "postgres" {
		return q
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(fmt.Sprint(n))
		} else {
			b.WriteByte(q[i])
		}
	}
	return b.String()
}

func (s *Store) exec(q string, args ...any) (sql.Result, error) {
	return s.db.Exec(s.rebind(q), args...)
}

func (s *Store) query(q string, args ...any) (*sql.Rows, error) {
	return s.db.Query(s.rebind(q), args...)
}

func (s *Store) queryRow(q string, args ...any) *sql.Row {
	return s.db.QueryRow(s.rebind(q), args...)
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// --- Runs ---

func (s *Store) PutRun(r *run.RehearsalRun) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	org, project, env := "", "", ""
	if r.Labels != nil {
		org, project, env = r.Labels["org"], r.Labels["project"], r.Labels["environment"]
	}
	now := s.now()
	_, err = s.exec(`
INSERT INTO runs(id, idempotency_key, org, project, environment, payload, phase, updated_at, created_at)
VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  payload=excluded.payload, phase=excluded.phase, updated_at=excluded.updated_at,
  org=excluded.org, project=excluded.project, environment=excluded.environment
`, r.ID, nullStr(r.IdempotencyKey), org, project, env, string(raw), string(r.Status.Phase), now, r.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) GetRun(id string) (*run.RehearsalRun, error) {
	var payload string
	err := s.queryRow(`SELECT payload FROM runs WHERE id=?`, id).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r run.RehearsalRun
	if err := json.Unmarshal([]byte(payload), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) GetRunByIdempotency(key string) (*run.RehearsalRun, error) {
	if key == "" {
		return nil, nil
	}
	var payload string
	err := s.queryRow(`SELECT payload FROM runs WHERE idempotency_key=?`, key).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r run.RehearsalRun
	if err := json.Unmarshal([]byte(payload), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) ListRuns(org string) ([]*run.RehearsalRun, error) {
	var rows *sql.Rows
	var err error
	if org == "" {
		rows, err = s.query(`SELECT payload FROM runs ORDER BY updated_at DESC LIMIT 500`)
	} else {
		rows, err = s.query(`SELECT payload FROM runs WHERE org=? ORDER BY updated_at DESC LIMIT 500`, org)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*run.RehearsalRun
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var r run.RehearsalRun
		if err := json.Unmarshal([]byte(payload), &r); err != nil {
			continue
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// --- Clusters / Policies ---

func (s *Store) PutCluster(c Cluster) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.exec(`
INSERT INTO clusters(name, org, payload, created_at) VALUES(?,?,?,?)
ON CONFLICT(org, name) DO UPDATE SET payload=excluded.payload
`, c.Name, c.Org, string(raw), c.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListClusters(org string) ([]Cluster, error) {
	rows, err := s.query(`SELECT payload FROM clusters WHERE org=? OR ?='' ORDER BY name`, org, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Cluster
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var c Cluster
		if json.Unmarshal([]byte(payload), &c) == nil {
			if org != "" && c.Org != org {
				continue
			}
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Store) PutPolicy(id, org string, p map[string]any) error {
	p["id"] = id
	p["org"] = org
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.exec(`
INSERT INTO policies(id, org, payload, created_at) VALUES(?,?,?,?)
ON CONFLICT(org, id) DO UPDATE SET payload=excluded.payload
`, id, org, string(raw), s.now())
	return err
}

func (s *Store) ListPolicies(org string) ([]map[string]any, error) {
	rows, err := s.query(`SELECT payload FROM policies WHERE org=?`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var m map[string]any
		if json.Unmarshal([]byte(payload), &m) == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

// GetOrgPolicy returns the first policy for org (product: one active policy per org).
func (s *Store) GetOrgPolicy(org string) (map[string]any, error) {
	var payload string
	err := s.queryRow(`SELECT payload FROM policies WHERE org=? ORDER BY created_at DESC LIMIT 1`, org).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// --- Calibration ---

func (s *Store) RecordCalibration(o calibrate.Outcome) error {
	_, err := s.exec(`INSERT INTO calibration(scenario, predicted, observed, verified, created_at) VALUES(?,?,?,?,?)`,
		o.Scenario, boolInt(o.Predicted), boolInt(o.Observed), boolInt(o.Verified), s.now())
	return err
}

func (s *Store) CalibrationReport() calibrate.Report {
	st := calibrate.NewStore()
	rows, err := s.query(`SELECT scenario, predicted, observed, verified FROM calibration`)
	if err != nil {
		return st.Report()
	}
	defer rows.Close()
	for rows.Next() {
		var sc string
		var p, o, v int
		if err := rows.Scan(&sc, &p, &o, &v); err != nil {
			continue
		}
		st.Record(calibrate.Outcome{Scenario: sc, Predicted: p == 1, Observed: o == 1, Verified: v == 1})
	}
	return st.Report()
}

// --- Audit ---

func (s *Store) Audit(actor, action, target, detail string) error {
	_, err := s.exec(`INSERT INTO audit(time, actor, action, target, detail) VALUES(?,?,?,?,?)`,
		s.now(), actor, action, target, detail)
	return err
}

// --- Jobs ---

// Job is a durable work item.
type Job struct {
	ID          string
	Kind        string
	RunID       string
	Org         string
	Status      string // pending|leased|done|failed
	Attempts    int
	MaxAttempts int
	Payload     string
	LastError   string
}

func (s *Store) Enqueue(kind, runID, org, payload string) (string, error) {
	id := fmt.Sprintf("job-%d", time.Now().UTC().UnixNano())
	now := s.now()
	_, err := s.exec(`
INSERT INTO jobs(id, kind, run_id, org, status, attempts, max_attempts, payload, created_at, updated_at)
VALUES(?,?,?,?, 'pending', 0, 5, ?, ?, ?)
`, id, kind, runID, org, payload, now, now)
	return id, err
}

// ClaimJob leases the next pending job (SKIP LOCKED style for sqlite: update where pending).
func (s *Store) ClaimJob(ctx context.Context, holder string, ttl time.Duration) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	// reclaim expired leases
	_, _ = tx.Exec(s.rebind(`UPDATE jobs SET status='pending', lease_holder=NULL, lease_until=NULL
		WHERE status='leased' AND lease_until IS NOT NULL AND lease_until < ?`), now.Format(time.RFC3339Nano))

	var j Job
	var payload sql.NullString
	err = tx.QueryRow(s.rebind(`
SELECT id, kind, run_id, org, status, attempts, max_attempts, COALESCE(payload,''), COALESCE(last_error,'')
FROM jobs WHERE status='pending' ORDER BY created_at ASC LIMIT 1
`)).Scan(&j.ID, &j.Kind, &j.RunID, &j.Org, &j.Status, &j.Attempts, &j.MaxAttempts, &payload, &j.LastError)
	if err == sql.ErrNoRows {
		_ = tx.Commit()
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.Payload = payload.String
	until := now.Add(ttl).Format(time.RFC3339Nano)
	res, err := tx.Exec(s.rebind(`UPDATE jobs SET status='leased', lease_holder=?, lease_until=?, attempts=attempts+1, updated_at=?
		WHERE id=? AND status='pending'`), holder, until, s.now(), j.ID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		_ = tx.Commit()
		return nil, nil
	}
	j.Attempts++
	j.Status = "leased"
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Store) CompleteJob(id string) error {
	_, err := s.exec(`UPDATE jobs SET status='done', lease_holder=NULL, lease_until=NULL, updated_at=? WHERE id=?`, s.now(), id)
	return err
}

func (s *Store) FailJob(id, lastErr string) error {
	// requeue if attempts remaining
	var attempts, max int
	_ = s.queryRow(`SELECT attempts, max_attempts FROM jobs WHERE id=?`, id).Scan(&attempts, &max)
	status := "pending"
	if attempts >= max {
		status = "failed"
	}
	_, err := s.exec(`UPDATE jobs SET status=?, last_error=?, lease_holder=NULL, lease_until=NULL, updated_at=? WHERE id=?`,
		status, lastErr, s.now(), id)
	return err
}

func (s *Store) JobStats() (pending, leased, done, failed int) {
	_ = s.queryRow(`SELECT COUNT(*) FROM jobs WHERE status='pending'`).Scan(&pending)
	_ = s.queryRow(`SELECT COUNT(*) FROM jobs WHERE status='leased'`).Scan(&leased)
	_ = s.queryRow(`SELECT COUNT(*) FROM jobs WHERE status='done'`).Scan(&done)
	_ = s.queryRow(`SELECT COUNT(*) FROM jobs WHERE status='failed'`).Scan(&failed)
	return
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// DB exposes underlying handle for health checks.
func (s *Store) DB() *sql.DB { return s.db }
