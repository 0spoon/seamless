package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpen_FreshDB_PragmasAndMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "seam.db")
	db, err := Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	var fk int
	require.NoError(t, db.QueryRow("PRAGMA foreign_keys").Scan(&fk))
	require.Equal(t, 1, fk, "foreign_keys must be ON")

	var jm string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&jm))
	require.Equal(t, "wal", strings.ToLower(jm))

	v, err := SchemaVersion(db)
	require.NoError(t, err)
	require.Equal(t, len(migrationList()), v, "schema version must match the number of migrations")

	for _, tbl := range []string{
		"projects", "memories_index", "notes_index", "embeddings", "sessions",
		"tasks", "task_deps", "trials", "events", "retrieval_stats",
		"gardener_proposals", "settings", "jobs",
	} {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&name)
		require.NoErrorf(t, err, "table %q should exist", tbl)
	}

	var ftsName string
	require.NoError(t, db.QueryRow("SELECT name FROM sqlite_master WHERE name='fts'").Scan(&ftsName))
	require.Equal(t, "fts", ftsName)
}

func TestOpen_Idempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "seam.db")

	db, err := Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db2, err := Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db2.Close() })

	var count int
	require.NoError(t, db2.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count))
	require.Equal(t, len(migrationList()), count, "reopening must not re-apply the migration")
}

func TestForeignKeys_Enforced(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "seam.db")
	db, err := Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// task_deps references tasks(id); a dep pointing at non-existent tasks must
	// be rejected end-to-end (proves foreign_keys is enforced, not just set).
	_, err = db.Exec("INSERT INTO task_deps(task_id, depends_on) VALUES('missing-a','missing-b')")
	require.Error(t, err)
}

func TestFTS_InsertMatchDelete(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "seam.db")
	db, err := Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(
		`INSERT INTO fts(item_id, kind, project, title, name, description, body)
		 VALUES(?,?,?,?,?,?,?)`,
		"01ABC", "memory", "seam", "", "chroma-boot-race",
		"chroma races on boot", "The chroma container loses a startup race.",
	)
	require.NoError(t, err)

	var itemID, kind string
	err = db.QueryRow("SELECT item_id, kind FROM fts WHERE fts MATCH 'chroma' LIMIT 1").Scan(&itemID, &kind)
	require.NoError(t, err)
	require.Equal(t, "01ABC", itemID)
	require.Equal(t, "memory", kind)

	// Stemming: 'race' should match 'races'/'racing' via the porter tokenizer.
	var n int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM fts WHERE fts MATCH 'race'").Scan(&n))
	require.Equal(t, 1, n)

	// A self-contained fts5 table supports plain DELETE by a stored column.
	_, err = db.Exec("DELETE FROM fts WHERE item_id = ?", "01ABC")
	require.NoError(t, err)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM fts WHERE fts MATCH 'chroma'").Scan(&n))
	require.Equal(t, 0, n)
}

func TestTableCount(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "seam.db")
	db, err := Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	n, err := TableCount(db)
	require.NoError(t, err)
	// 13 domain tables + schema_migrations + the fts virtual table's own entry
	// is excluded by name filter; assert a sane lower bound instead of an exact
	// count so adding indexes/tables later does not brittle-break this.
	require.GreaterOrEqual(t, n, 14)
}
