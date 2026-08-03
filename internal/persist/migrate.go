package persist

import (
	"fmt"
	"strings"
)

// CurrentSchema is the highest applied migration version.
const CurrentSchema = 4

// Migration is a versioned SQL change (idempotent where possible).
type Migration struct {
	Version  int
	Name     string
	SQLite   string
	Postgres string
}

// migrations is ordered ascending. Version 2 is the trust-boundary base (schemaV2).
var migrations = []Migration{
	{
		Version: 3,
		Name:    "ops_indexes_retention",
		SQLite: `
CREATE INDEX IF NOT EXISTS idx_audit_org_time ON audit(org, time);
CREATE INDEX IF NOT EXISTS idx_jobs_org_status ON jobs(org, status);
CREATE INDEX IF NOT EXISTS idx_runs_org_updated ON runs(org, updated_at);
`,
		Postgres: `
CREATE INDEX IF NOT EXISTS idx_audit_org_time ON audit(org, time);
CREATE INDEX IF NOT EXISTS idx_jobs_org_status ON jobs(org, status);
CREATE INDEX IF NOT EXISTS idx_runs_org_updated ON runs(org, updated_at);
`,
	},
	{
		Version: 4,
		Name:    "run_version_optimistic",
		SQLite: `
ALTER TABLE runs ADD COLUMN version INTEGER NOT NULL DEFAULT 0;
`,
		Postgres: `
ALTER TABLE runs ADD COLUMN IF NOT EXISTS version INTEGER NOT NULL DEFAULT 0;
`,
	},
}

func (s *Store) schemaVersion() int {
	var v int
	err := s.queryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0
	}
	return v
}

func (s *Store) applyMigrations() error {
	// Base schema (v2) already applied in migrate().
	cur := s.schemaVersion()
	if cur < 2 {
		// ensure version 2 recorded
		if err := s.recordMigration(2); err != nil {
			return err
		}
		cur = 2
	}
	for _, m := range migrations {
		if m.Version <= cur {
			continue
		}
		sqlText := m.SQLite
		if s.dialect == "postgres" {
			sqlText = m.Postgres
		}
		for _, stmt := range strings.Split(sqlText, ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := s.db.Exec(stmt); err != nil {
				msg := strings.ToLower(err.Error())
				if strings.Contains(msg, "already exists") || strings.Contains(msg, "duplicate column") {
					continue
				}
				return fmt.Errorf("migration %d %s: %w", m.Version, m.Name, err)
			}
		}
		if err := s.recordMigration(m.Version); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) recordMigration(v int) error {
	if s.dialect == "postgres" {
		_, err := s.db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES($1, $2) ON CONFLICT (version) DO NOTHING`, v, s.now())
		return err
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(?, ?)`, v, s.now())
	return err
}

// SchemaVersion returns applied max version (for readiness / ops).
func (s *Store) SchemaVersion() int { return s.schemaVersion() }
