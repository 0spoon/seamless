// Package store owns the SQLite database: connection setup, schema migrations,
// FTS5, and embedding storage. It takes a database path (not the config) to stay
// a leaf dependency. Files remain the source of truth for durable knowledge; the
// *_index tables and the fts virtual table are rebuildable mirrors.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open opens (creating if needed) the SQLite database at dbPath, applies PRAGMAs
// via the DSN so every pooled connection inherits them, runs migrations, and
// returns the handle. The caller is responsible for closing the *sql.DB.
func Open(dbPath string) (*sql.DB, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store.Open: create data dir: %w", err)
		}
	}

	// Set PRAGMAs on the DSN (not via db.Exec) so foreign_keys and busy_timeout
	// apply to every connection the pool opens, not just the first. mmap_size
	// serves reads from file-backed mapped pages instead of one pread per 4KB
	// page -- the embeddings full scan behind CosineSearch outgrows the default
	// 2MB page cache immediately, and mapped pages are OS-evictable, so this is
	// an address-space reservation, not a 256MB heap commitment.
	dsn := "file:" + url.PathEscape(dbPath) +
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)" +
		"&_pragma=mmap_size(268435456)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store.Open: %w", err)
	}

	// modernc SQLite is a single-writer embedded engine; capping to one
	// connection serializes writes and avoids SQLITE_BUSY at our scale.
	db.SetMaxOpenConns(1)

	if err := migrate(db, migrationList()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store.Open: %w", err)
	}
	return db, nil
}

// SchemaVersion returns the highest applied migration version.
func SchemaVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&v); err != nil {
		return 0, fmt.Errorf("store.SchemaVersion: %w", err)
	}
	return v, nil
}

// TableCount returns the number of user tables (excluding SQLite internals and
// FTS shadow tables). Useful for the doctor's DB check.
func TableCount(db *sql.DB) (int, error) {
	var n int
	const q = `SELECT COUNT(*) FROM sqlite_master
	           WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'fts_%'`
	if err := db.QueryRow(q).Scan(&n); err != nil {
		return 0, fmt.Errorf("store.TableCount: %w", err)
	}
	return n, nil
}
