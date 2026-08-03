package persist

const schemaSQL = `
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  idempotency_key TEXT,
  org TEXT NOT NULL DEFAULT '',
  project TEXT NOT NULL DEFAULT '',
  environment TEXT NOT NULL DEFAULT '',
  payload TEXT NOT NULL,
  phase TEXT NOT NULL DEFAULT 'Pending',
  updated_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_idem ON runs(idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key != '';
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
  lease_holder TEXT,
  lease_until TEXT,
  last_error TEXT,
  payload TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status, lease_until);

CREATE TABLE IF NOT EXISTS calibration (
  scenario TEXT NOT NULL,
  predicted INTEGER NOT NULL,
  observed INTEGER NOT NULL,
  verified INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cal_scenario ON calibration(scenario);

CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  time TEXT NOT NULL,
  actor TEXT,
  action TEXT,
  target TEXT,
  detail TEXT
);

CREATE TABLE IF NOT EXISTS blobs (
  digest TEXT PRIMARY KEY,
  path TEXT NOT NULL,
  bytes INTEGER NOT NULL,
  content_type TEXT,
  created_at TEXT NOT NULL
);
`
