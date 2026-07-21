package store

import (
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed migrations/001_initial.sql
var migration001 string

//go:embed migrations/002_task_plans_claims.sql
var migration002 string

//go:embed migrations/003_gardener_consolidate.sql
var migration003 string

//go:embed migrations/004_project_topology.sql
var migration004 string

//go:embed migrations/005_abandon_plan.sql
var migration005 string

//go:embed migrations/006_session_expired.sql
var migration006 string

//go:embed migrations/007_open_claims_repair.sql
var migration007 string

//go:embed migrations/008_tasks_claimed_by_index.sql
var migration008 string

//go:embed migrations/009_model_attribution.sql
var migration009 string

//go:embed migrations/010_session_external_identity.sql
var migration010 string

//go:embed migrations/011_session_token_usage.sql
var migration011 string

//go:embed migrations/012_favorites.sql
var migration012 string

//go:embed migrations/013_retrieval_utility.sql
var migration013 string

//go:embed migrations/014_memory_wanted.sql
var migration014 string

//go:embed migrations/015_tool_error.sql
var migration015 string

// Migration is a single numbered schema migration.
type Migration struct {
	Version int
	SQL     string
}

// migrationList returns the ordered migrations. Append new numbered files here
// (with a matching go:embed) -- NEVER edit an already-applied migration.
func migrationList() []Migration {
	return []Migration{
		{Version: 1, SQL: migration001},
		{Version: 2, SQL: migration002},
		{Version: 3, SQL: migration003},
		{Version: 4, SQL: migration004},
		{Version: 5, SQL: migration005},
		{Version: 6, SQL: migration006},
		{Version: 7, SQL: migration007},
		{Version: 8, SQL: migration008},
		{Version: 9, SQL: migration009},
		{Version: 10, SQL: migration010},
		{Version: 11, SQL: migration011},
		{Version: 12, SQL: migration012},
		{Version: 13, SQL: migration013},
		{Version: 14, SQL: migration014},
		{Version: 15, SQL: migration015},
	}
}

// migrate applies every migration whose version exceeds the current max, each
// inside its own transaction, recording the version in schema_migrations.
// Ported from Seam v1 (migrations/migrate.go); the rarely-used PreHook was
// dropped -- add it back only if a future migration needs pre-transaction DDL.
func migrate(db *sql.DB, ms []Migration) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("store.migrate: create tracking table: %w", err)
	}

	var current int
	if err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current); err != nil {
		return fmt.Errorf("store.migrate: read current version: %w", err)
	}

	for _, m := range ms {
		if m.Version <= current {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("store.migrate: begin v%d: %w", m.Version, err)
		}
		if _, err := tx.Exec(m.SQL); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store.migrate: apply v%d: %w", m.Version, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", m.Version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store.migrate: record v%d: %w", m.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store.migrate: commit v%d: %w", m.Version, err)
		}
	}
	return nil
}
