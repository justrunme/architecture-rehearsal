package persist

// schemaV2 is the trust-boundary schema (v1.2.1+).
// runs PK = (org, id); idempotency unique per org; calibration tenant-scoped.
const schemaV2 = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
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
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_idem_org ON runs(org, idempotency_key)
  WHERE idempotency_key IS NOT NULL AND idempotency_key != '';
CREATE INDEX IF NOT EXISTS idx_runs_org ON runs(org);

CREATE TABLE IF NOT EXISTS clusters (
  name TEXT NOT NULL,
  org TEXT NOT NULL DEFAULT '',
  payload TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (org, name)
);

CREATE TABLE IF NOT EXISTS policies (
  id TEXT NOT NULL,
  org TEXT NOT NULL DEFAULT '',
  payload TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (org, id)
);

CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  run_id TEXT NOT NULL,
  org TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending',
  attempts INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 5,
  fence_token INTEGER NOT NULL DEFAULT 0,
  lease_holder TEXT,
  lease_until TEXT,
  last_error TEXT,
  payload TEXT,
  operation_id TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status, lease_until);
CREATE INDEX IF NOT EXISTS idx_jobs_run ON jobs(org, run_id, status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_op ON jobs(org, operation_id)
  WHERE operation_id IS NOT NULL AND operation_id != '';

CREATE TABLE IF NOT EXISTS calibration (
  org TEXT NOT NULL DEFAULT '',
  scenario TEXT NOT NULL,
  predicted INTEGER NOT NULL,
  observed INTEGER NOT NULL,
  verified INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cal_org_scenario ON calibration(org, scenario);

CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  time TEXT NOT NULL,
  actor TEXT,
  action TEXT,
  target TEXT,
  detail TEXT,
  org TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS blobs (
  digest TEXT PRIMARY KEY,
  path TEXT NOT NULL,
  bytes INTEGER NOT NULL,
  content_type TEXT,
  created_at TEXT NOT NULL
);
`

// Legacy schema detection (v1.2.0 single-column PK).
const legacyRunsCheck = `SELECT sql FROM sqlite_master WHERE type='table' AND name='runs'`
