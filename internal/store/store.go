// Package store owns the SQLite database: connection setup, schema migrations,
// FTS5, and embedding storage. It takes a database path (not the config) to stay
// a leaf dependency. Files remain the source of truth for durable knowledge; the
// *_index tables and the fts virtual table are rebuildable mirrors.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// isUniqueViolation reports whether err is a SQLite UNIQUE (or PRIMARY KEY)
// constraint failure, via the driver's typed error code rather than matching on
// the message text (string-matching a driver error is banned by AGENTS.md: the
// text is not part of the driver's contract).
//
// It lives here rather than next to any one caller because a UNIQUE column is
// not a projects concept -- CreateProject and CreateSession both need it to turn
// a constraint failure into their own sentinel.
func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	code := se.Code()
	return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

// Open opens (creating if needed) the SQLite database at dbPath, applies PRAGMAs
// via the DSN so every pooled connection inherits them, runs migrations, and
// returns the handle. The caller is responsible for closing the *sql.DB.
func Open(dbPath string) (*sql.DB, error) {
	// 0700: the data dir holds the corpus and the DB (sessions, events, tool
	// bodies, embeddings). Owner-only, so a traversable home does not hand
	// another local account a path into it. (Audit L5.)
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("store.Open: create data dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dsn(dbPath))
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

// dsn builds the connection string shared by Open and OpenExisting.
//
// PRAGMAs ride on the DSN (not a db.Exec) so foreign_keys and busy_timeout
// apply to every connection the pool opens, not just the first. mmap_size
// serves reads from file-backed mapped pages instead of one pread per 4KB
// page -- the embeddings full scan behind CosineSearch outgrows the default
// 2MB page cache immediately, and mapped pages are OS-evictable, so this is
// an address-space reservation, not a 256MB heap commitment.
//
// It is one function rather than two copies on purpose: OpenExisting differs
// from Open in exactly one thing (it never migrates), and a drifting PRAGMA
// set would make that a second, silent difference.
func dsn(dbPath string) string {
	return "file:" + url.PathEscape(dbPath) +
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)" +
		"&_pragma=mmap_size(268435456)"
}

// OpenExisting opens an existing SQLite database with the same DSN as Open but
// WITHOUT running migrations, and reports os.ErrNotExist when the file is
// absent instead of creating it.
//
// This is the read-path handle for one-shot subcommands that must not change
// the database they are reading. `seamlessd export` is the motivating caller: a
// newer CLI binary run against a data dir served by an older running daemon
// would otherwise migrate the live schema out from under it as a side effect of
// taking a backup. Anything that legitimately owns the schema calls Open.
func OpenExisting(dbPath string) (*sql.DB, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("store.OpenExisting: %w", err)
	}

	db, err := sql.Open("sqlite", dsn(dbPath))
	if err != nil {
		return nil, fmt.Errorf("store.OpenExisting: %w", err)
	}
	db.SetMaxOpenConns(1)

	// sql.Open is lazy, so an unreadable or non-SQLite file would otherwise
	// surface as a confusing failure inside the caller's first query.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store.OpenExisting: %w", err)
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
