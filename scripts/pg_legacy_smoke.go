//go:build ignore

// pg_legacy_smoke simulates v1.2.0 global PK schema then opens with current Store.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/persist"
	"github.com/justrunme/architecture-rehearsal/internal/run"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := os.Getenv("REHEARSAL_DATABASE_URL")
	if dsn == "" {
		panic("REHEARSAL_DATABASE_URL required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		panic(err)
	}
	// Drop and create legacy v1.2.0-style schema
	_, _ = db.Exec(`DROP TABLE IF EXISTS jobs, calibration, audit, blobs, policies, clusters, runs, schema_migrations CASCADE`)
	_, err = db.Exec(`
CREATE TABLE runs (
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
CREATE UNIQUE INDEX idx_runs_idem ON runs(idempotency_key);
`)
	if err != nil {
		panic(err)
	}
	// seed two orgs would conflict on id under legacy — seed one
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO runs(id, idempotency_key, org, project, environment, payload, phase, updated_at, created_at)
VALUES('shared','ik-a','org-a','','','{"id":"shared","labels":{"org":"org-a"},"status":{"phase":"Pending"}}','Pending',$1,$2)`, now, now)
	if err != nil {
		panic(err)
	}
	_ = db.Close()

	// Open with new code — must upgrade to (org,id) and allow same id in other org
	s, err := persist.Open(dsn, "")
	if err != nil {
		panic(fmt.Sprintf("open after legacy: %v", err))
	}
	defer s.Close()

	// same id different org
	rr := run.NewRun("shared", "ik-b", run.Spec{})
	rr.Labels = map[string]string{"org": "org-b"}
	if err := s.CreateRun(rr); err != nil {
		panic(fmt.Sprintf("create org-b same id: %v", err))
	}
	a, _ := s.GetRun("org-a", "shared")
	b, _ := s.GetRun("org-b", "shared")
	if a == nil || b == nil {
		panic("missing runs after migration")
	}
	if a.Labels["org"] != "org-a" || b.Labels["org"] != "org-b" {
		panic("org isolation broken after migration")
	}
	fmt.Println("postgres legacy migration ok schema", s.SchemaVersion())
}
