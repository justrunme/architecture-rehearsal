// Package persist provides durable storage for the control plane.
// v1.2.1: tenant-aware identity (org, id), org-scoped idempotency, no cross-tenant overwrite.
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
	db      *sql.DB
	Blob    Blob // filesystem or S3-compatible
	dialect string // sqlite|postgres
}

// Open opens a database.
// dsn: empty/file path → sqlite; postgres:// → pgx.
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
		if err := registerPGX(); err != nil {
			return nil, err
		}
		openDSN = dsn
	} else if !strings.HasPrefix(dsn, "file:") && dsn != "" && !strings.Contains(dsn, "://") {
		if dir := filepath.Dir(dsn); dir != "." && dir != "" {
			_ = os.MkdirAll(dir, 0o755)
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
	sqlText := schemaV2
	if s.dialect == "postgres" {
		sqlText = strings.ReplaceAll(sqlText, "INTEGER PRIMARY KEY AUTOINCREMENT", "BIGSERIAL PRIMARY KEY")
		sqlText = strings.ReplaceAll(sqlText, "idempotency_key != ''", "idempotency_key <> ''")
		sqlText = strings.ReplaceAll(sqlText, "operation_id != ''", "operation_id <> ''")
	}
	// Detect legacy v1.2.0 runs table (global PK on id only) and rebuild.
	if s.dialect == "sqlite" {
		if err := s.upgradeLegacySQLite(); err != nil {
			return err
		}
	}
	if s.dialect == "postgres" {
		if err := s.upgradeLegacyPostgres(); err != nil {
			return err
		}
	}
	for _, stmt := range strings.Split(sqlText, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := s.db.Exec(stmt); err != nil {
			// ignore "already exists" style for concurrent open
			if !strings.Contains(err.Error(), "already exists") {
				return fmt.Errorf("migrate: %w\nSQL: %s", err, stmt)
			}
		}
	}
	// Record schema version 2 then apply ordered migrations (3+)
	if err := s.recordMigration(2); err != nil {
		return err
	}
	return s.applyMigrations()
}

func (s *Store) upgradeLegacySQLite() error {
	var createSQL sql.NullString
	err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='runs'`).Scan(&createSQL)
	if err == sql.ErrNoRows || !createSQL.Valid {
		return nil // fresh
	}
	if err != nil {
		// table may not exist yet
		return nil
	}
	// Legacy: PRIMARY KEY on id alone (not composite)
	if strings.Contains(createSQL.String, "PRIMARY KEY (org, id)") {
		return nil
	}
	if !strings.Contains(createSQL.String, "PRIMARY KEY") {
		return nil
	}
	// Rebuild tables with tenant keys. Data preserved where possible.
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmts := []string{
		`ALTER TABLE runs RENAME TO runs_legacy`,
		`CREATE TABLE runs (
  org TEXT NOT NULL DEFAULT '',
  id TEXT NOT NULL,
  idempotency_key TEXT,
  project TEXT NOT NULL DEFAULT '',
  environment TEXT NOT NULL DEFAULT '',
  payload TEXT NOT NULL,
  phase TEXT NOT NULL DEFAULT 'Pending',
  updated_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (org, id)
)`,
		`INSERT INTO runs(org, id, idempotency_key, project, environment, payload, phase, updated_at, created_at)
 SELECT COALESCE(org,''), id, idempotency_key, COALESCE(project,''), COALESCE(environment,''), payload, phase, updated_at, created_at FROM runs_legacy`,
		`DROP TABLE runs_legacy`,
		`DROP INDEX IF EXISTS idx_runs_idem`,
		// calibration: add org if missing
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("legacy upgrade: %w (%s)", err, q)
		}
	}
	// calibration rebuild if no org column
	var calSQL sql.NullString
	_ = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='calibration'`).Scan(&calSQL)
	if calSQL.Valid && !strings.Contains(calSQL.String, "org") {
		_, _ = tx.Exec(`ALTER TABLE calibration RENAME TO calibration_legacy`)
		_, _ = tx.Exec(`CREATE TABLE calibration (
  org TEXT NOT NULL DEFAULT '',
  scenario TEXT NOT NULL,
  predicted INTEGER NOT NULL,
  observed INTEGER NOT NULL,
  verified INTEGER NOT NULL,
  created_at TEXT NOT NULL
)`)
		_, _ = tx.Exec(`INSERT INTO calibration(org, scenario, predicted, observed, verified, created_at)
 SELECT '', scenario, predicted, observed, verified, created_at FROM calibration_legacy`)
		_, _ = tx.Exec(`DROP TABLE calibration_legacy`)
	}
	// jobs: add fence_token / operation_id if missing
	var jobSQL sql.NullString
	_ = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='jobs'`).Scan(&jobSQL)
	if jobSQL.Valid && !strings.Contains(jobSQL.String, "fence_token") {
		_, _ = tx.Exec(`ALTER TABLE jobs ADD COLUMN fence_token INTEGER NOT NULL DEFAULT 0`)
	}
	if jobSQL.Valid && !strings.Contains(jobSQL.String, "operation_id") {
		_, _ = tx.Exec(`ALTER TABLE jobs ADD COLUMN operation_id TEXT`)
	}
	return tx.Commit()
}

// upgradeLegacyPostgres migrates v1.2.0 global PK (id) → composite (org, id).
func (s *Store) upgradeLegacyPostgres() error {
	// Detect legacy: runs has PK only on id (constraint_name runs_pkey with single column id).
	var n int
	err := s.db.QueryRow(`
SELECT COUNT(*) FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
WHERE tc.table_name = 'runs' AND tc.constraint_type = 'PRIMARY KEY'
`).Scan(&n)
	if err != nil {
		// table may not exist yet
		return nil
	}
	if n == 0 {
		return nil
	}
	var cols int
	_ = s.db.QueryRow(`
SELECT COUNT(*) FROM information_schema.key_column_usage
WHERE table_name = 'runs' AND constraint_name IN (
  SELECT constraint_name FROM information_schema.table_constraints
  WHERE table_name = 'runs' AND constraint_type = 'PRIMARY KEY'
)`).Scan(&cols)
	if cols != 1 {
		return nil // already composite or unknown
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmts := []string{
		`ALTER TABLE runs RENAME TO runs_legacy`,
		`CREATE TABLE runs (
  org TEXT NOT NULL DEFAULT '',
  id TEXT NOT NULL,
  idempotency_key TEXT,
  project TEXT NOT NULL DEFAULT '',
  environment TEXT NOT NULL DEFAULT '',
  payload TEXT NOT NULL,
  phase TEXT NOT NULL DEFAULT 'Pending',
  version INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (org, id)
)`,
		`INSERT INTO runs(org, id, idempotency_key, project, environment, payload, phase, updated_at, created_at)
 SELECT COALESCE(org,''), id, idempotency_key, COALESCE(project,''), COALESCE(environment,''), payload, phase, updated_at, created_at FROM runs_legacy`,
		`DROP TABLE runs_legacy`,
		`DROP INDEX IF EXISTS idx_runs_idem`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_idem_org ON runs(org, idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("postgres legacy upgrade: %w (%s)", err, q)
		}
	}
	// calibration org column
	var calHasOrg int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_name='calibration' AND column_name='org'`).Scan(&calHasOrg)
	if calHasOrg == 0 {
		_, _ = tx.Exec(`ALTER TABLE calibration ADD COLUMN org TEXT NOT NULL DEFAULT ''`)
	}
	// jobs fence/operation
	var hasFence int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_name='jobs' AND column_name='fence_token'`).Scan(&hasFence)
	if hasFence == 0 {
		_, _ = tx.Exec(`ALTER TABLE jobs ADD COLUMN fence_token INTEGER NOT NULL DEFAULT 0`)
	}
	var hasOp int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_name='jobs' AND column_name='operation_id'`).Scan(&hasOp)
	if hasOp == 0 {
		_, _ = tx.Exec(`ALTER TABLE jobs ADD COLUMN operation_id TEXT`)
	}
	return tx.Commit()
}

// JobIsActive reports whether a job is still leased by holder with fence (for cancel checks).
func (s *Store) JobIsActive(id, holder string, fence int64) (bool, error) {
	var status, leaseHolder sql.NullString
	var f int64
	err := s.queryRow(`SELECT status, COALESCE(lease_holder,''), fence_token FROM jobs WHERE id=?`, id).Scan(&status, &leaseHolder, &f)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status.String == "leased" && leaseHolder.String == holder && f == fence, nil
}

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

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func runOrg(r *run.RehearsalRun) string {
	if r == nil || r.Labels == nil {
		return ""
	}
	return r.Labels["org"]
}

// CreateRun inserts a new run. Returns ErrConflict if (org, id) already exists.
func (s *Store) CreateRun(r *run.RehearsalRun) error {
	if r == nil || r.ID == "" {
		return fmt.Errorf("run id required")
	}
	org := runOrg(r)
	if org == "" {
		return fmt.Errorf("run org required")
	}
	if r.Version == 0 {
		r.Version = 1
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	project, env := "", ""
	if r.Labels != nil {
		project, env = r.Labels["project"], r.Labels["environment"]
	}
	now := s.now()
	_, err = s.exec(`
INSERT INTO runs(org, id, idempotency_key, project, environment, payload, phase, version, updated_at, created_at)
VALUES(?,?,?,?,?,?,?,?,?,?)
`, org, r.ID, nullStr(r.IdempotencyKey), project, env, string(raw), string(r.Status.Phase), r.Version, now, r.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		// fallback without version column (pre-migration)
		if strings.Contains(err.Error(), "version") {
			_, err = s.exec(`
INSERT INTO runs(org, id, idempotency_key, project, environment, payload, phase, updated_at, created_at)
VALUES(?,?,?,?,?,?,?,?,?)
`, org, r.ID, nullStr(r.IdempotencyKey), project, env, string(raw), string(r.Status.Phase), now, r.CreatedAt.UTC().Format(time.RFC3339Nano))
			if err != nil && isUniqueViolation(err) {
				return ErrConflict
			}
			return err
		}
		return err
	}
	return nil
}

// UpdateRun updates an existing run with optimistic concurrency (version).
// Expected version is r.Version before increment; on success r.Version is incremented.
func (s *Store) UpdateRun(r *run.RehearsalRun) error {
	if r == nil || r.ID == "" {
		return fmt.Errorf("run id required")
	}
	org := runOrg(r)
	if org == "" {
		return fmt.Errorf("run org required")
	}
	expected := r.Version
	next := expected + 1
	if next < 1 {
		next = 1
	}
	r.Version = next
	raw, err := json.Marshal(r)
	if err != nil {
		r.Version = expected
		return err
	}
	project, env := "", ""
	if r.Labels != nil {
		project, env = r.Labels["project"], r.Labels["environment"]
	}
	// Optimistic: match previous version (0 matches any missing/legacy row)
	var res sql.Result
	if expected == 0 {
		res, err = s.exec(`
UPDATE runs SET payload=?, phase=?, project=?, environment=?, version=?, updated_at=?
WHERE org=? AND id=?
`, string(raw), string(r.Status.Phase), project, env, next, s.now(), org, r.ID)
	} else {
		res, err = s.exec(`
UPDATE runs SET payload=?, phase=?, project=?, environment=?, version=?, updated_at=?
WHERE org=? AND id=? AND version=?
`, string(raw), string(r.Status.Phase), project, env, next, s.now(), org, r.ID, expected)
	}
	if err != nil {
		r.Version = expected
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		r.Version = expected
		return ErrConflict // stale version or missing
	}
	return nil
}

// PutRun is UpdateRun if exists else CreateRun (same org only). Used by workers.
func (s *Store) PutRun(r *run.RehearsalRun) error {
	org := runOrg(r)
	existing, err := s.GetRun(org, r.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return s.CreateRun(r)
	}
	// refuse org reassignment
	if runOrg(existing) != org {
		return ErrConflict
	}
	return s.UpdateRun(r)
}

// GetRun returns a run only if it belongs to org.
func (s *Store) GetRun(org, id string) (*run.RehearsalRun, error) {
	if org == "" || id == "" {
		return nil, nil
	}
	var payload string
	err := s.queryRow(`SELECT payload FROM runs WHERE org=? AND id=?`, org, id).Scan(&payload)
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

// GetRunByIdempotency looks up within org only.
func (s *Store) GetRunByIdempotency(org, key string) (*run.RehearsalRun, error) {
	if org == "" || key == "" {
		return nil, nil
	}
	var payload string
	err := s.queryRow(`SELECT payload FROM runs WHERE org=? AND idempotency_key=?`, org, key).Scan(&payload)
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
	if org == "" {
		return nil, nil
	}
	rows, err := s.query(`SELECT payload FROM runs WHERE org=? ORDER BY updated_at DESC LIMIT 500`, org)
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
	if org == "" {
		return nil, nil
	}
	rows, err := s.query(`SELECT payload FROM clusters WHERE org=? ORDER BY name`, org)
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

// --- Calibration (org-scoped) ---

func (s *Store) RecordCalibration(org string, o calibrate.Outcome) error {
	if org == "" {
		return fmt.Errorf("org required for calibration")
	}
	_, err := s.exec(`INSERT INTO calibration(org, scenario, predicted, observed, verified, created_at) VALUES(?,?,?,?,?,?)`,
		org, o.Scenario, boolInt(o.Predicted), boolInt(o.Observed), boolInt(o.Verified), s.now())
	return err
}

func (s *Store) CalibrationReport(org string) calibrate.Report {
	st := calibrate.NewStore()
	if org == "" {
		return st.Report()
	}
	rows, err := s.query(`SELECT scenario, predicted, observed, verified FROM calibration WHERE org=?`, org)
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

func (s *Store) Audit(actor, action, target, detail, org string) error {
	_, err := s.exec(`INSERT INTO audit(time, actor, action, target, detail, org) VALUES(?,?,?,?,?,?)`,
		s.now(), actor, action, target, detail, org)
	return err
}

// --- Jobs ---

// Job is a durable work item.
type Job struct {
	ID          string
	Kind        string
	RunID       string
	Org         string
	Status      string // pending|leased|done|failed|cancelled
	Attempts    int
	MaxAttempts int
	FenceToken  int64
	Payload     string
	LastError   string
	OperationID string
}

// Enqueue creates a job. operationID enables exactly-once logical enqueue:
// INSERT then on unique violation SELECT existing id (no TOCTOU race).
func (s *Store) Enqueue(kind, runID, org, payload, operationID string) (string, error) {
	if org == "" {
		return "", fmt.Errorf("org required")
	}
	id := fmt.Sprintf("job-%d", time.Now().UTC().UnixNano())
	now := s.now()
	_, err := s.exec(`
INSERT INTO jobs(id, kind, run_id, org, status, attempts, max_attempts, fence_token, payload, operation_id, created_at, updated_at)
VALUES(?,?,?,?, 'pending', 0, 5, 0, ?, ?, ?, ?)
`, id, kind, runID, org, payload, nullStr(operationID), now, now)
	if err == nil {
		return id, nil
	}
	if operationID != "" && isUniqueViolation(err) {
		var existing string
		qerr := s.queryRow(`SELECT id FROM jobs WHERE org=? AND operation_id=?`, org, operationID).Scan(&existing)
		if qerr == nil && existing != "" {
			return existing, nil
		}
		if qerr != nil {
			return "", qerr
		}
	}
	return "", err
}

// ClaimJob leases the next pending job with a fencing token.
func (s *Store) ClaimJob(ctx context.Context, holder string, ttl time.Duration) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	_, _ = tx.Exec(s.rebind(`UPDATE jobs SET status='pending', lease_holder=NULL, lease_until=NULL
		WHERE status='leased' AND lease_until IS NOT NULL AND lease_until < ?`), now.Format(time.RFC3339Nano))

	var j Job
	var payload sql.NullString
	var op sql.NullString
	err = tx.QueryRow(s.rebind(`
SELECT id, kind, run_id, org, status, attempts, max_attempts, fence_token, COALESCE(payload,''), COALESCE(last_error,''), COALESCE(operation_id,'')
FROM jobs WHERE status='pending' ORDER BY created_at ASC LIMIT 1
`)).Scan(&j.ID, &j.Kind, &j.RunID, &j.Org, &j.Status, &j.Attempts, &j.MaxAttempts, &j.FenceToken, &payload, &j.LastError, &op)
	if err == sql.ErrNoRows {
		_ = tx.Commit()
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.Payload = payload.String
	j.OperationID = op.String
	until := now.Add(ttl).Format(time.RFC3339Nano)
	newFence := j.FenceToken + 1
	res, err := tx.Exec(s.rebind(`UPDATE jobs SET status='leased', lease_holder=?, lease_until=?, attempts=attempts+1,
		fence_token=?, updated_at=? WHERE id=? AND status='pending'`), holder, until, newFence, s.now(), j.ID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		_ = tx.Commit()
		return nil, nil
	}
	j.Attempts++
	j.FenceToken = newFence
	j.Status = "leased"
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &j, nil
}

// RenewLease extends lease if holder still owns the fence token.
func (s *Store) RenewLease(id, holder string, fence int64, ttl time.Duration) error {
	until := time.Now().UTC().Add(ttl).Format(time.RFC3339Nano)
	res, err := s.exec(`UPDATE jobs SET lease_until=?, updated_at=?
		WHERE id=? AND status='leased' AND lease_holder=? AND fence_token=?`,
		until, s.now(), id, holder, fence)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrStaleFence
	}
	return nil
}

func (s *Store) CompleteJob(id string, fence int64) error {
	res, err := s.exec(`UPDATE jobs SET status='done', lease_holder=NULL, lease_until=NULL, updated_at=?
		WHERE id=? AND fence_token=? AND status='leased'`, s.now(), id, fence)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrStaleFence
	}
	return nil
}

func (s *Store) FailJob(id string, fence int64, lastErr string) error {
	var attempts, max int
	var curFence int64
	err := s.queryRow(`SELECT attempts, max_attempts, fence_token FROM jobs WHERE id=?`, id).Scan(&attempts, &max, &curFence)
	if err != nil {
		return err
	}
	if curFence != fence {
		return ErrStaleFence
	}
	status := "pending"
	if attempts >= max {
		status = "failed"
	}
	_, err = s.exec(`UPDATE jobs SET status=?, last_error=?, lease_holder=NULL, lease_until=NULL, updated_at=?
		WHERE id=? AND fence_token=?`, status, lastErr, s.now(), id, fence)
	return err
}

// CancelJobsForRun cancels pending/leased jobs for a run (tenant-scoped).
func (s *Store) CancelJobsForRun(org, runID string) (int, error) {
	res, err := s.exec(`UPDATE jobs SET status='cancelled', lease_holder=NULL, lease_until=NULL, updated_at=?, last_error='cancelled'
		WHERE org=? AND run_id=? AND status IN ('pending','leased')`, s.now(), org, runID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) JobStats() (pending, leased, done, failed int) {
	_ = s.queryRow(`SELECT COUNT(*) FROM jobs WHERE status='pending'`).Scan(&pending)
	_ = s.queryRow(`SELECT COUNT(*) FROM jobs WHERE status='leased'`).Scan(&leased)
	_ = s.queryRow(`SELECT COUNT(*) FROM jobs WHERE status='done'`).Scan(&done)
	_ = s.queryRow(`SELECT COUNT(*) FROM jobs WHERE status='failed'`).Scan(&failed)
	return
}

// Ping checks database connectivity for readiness.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) DB() *sql.DB { return s.db }

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

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "constraint") || strings.Contains(msg, "duplicate")
}
