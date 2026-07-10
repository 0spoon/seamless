package files

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

func newIndexer(t *testing.T) (*Indexer, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return NewIndexer(db), db
}

// ftsMatch returns the item ids matching a query, in fts order.
func ftsMatch(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT item_id FROM fts WHERE fts MATCH ? ORDER BY rank`, query)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

func TestIndexMemory(t *testing.T) {
	ix, db := newIndexer(t)
	ctx := context.Background()

	m := sampleMemory()
	m.FilePath = MemoryRelPath(m.Project, m.Name)
	m.ContentHash = "hash1"
	require.NoError(t, ix.IndexMemory(ctx, m))

	var (
		kind, name, desc, project, fp, tags, validFrom, hash string
		invalidAt, supersededBy                              sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT kind, name, description, project, file_path, tags,
		       valid_from, invalid_at, superseded_by, content_hash
		FROM memories_index WHERE id = ?`, m.ID).
		Scan(&kind, &name, &desc, &project, &fp, &tags, &validFrom, &invalidAt, &supersededBy, &hash)
	require.NoError(t, err)
	require.Equal(t, "gotcha", kind)
	require.Equal(t, "chroma-boot-race", name)
	require.Equal(t, "seam", project)
	require.Equal(t, "memory/seam/chroma-boot-race.md", fp)
	require.Equal(t, []string{"chroma", "boot"}, parseTags(tags))
	require.False(t, invalidAt.Valid, "active memory has NULL invalid_at")
	require.False(t, supersededBy.Valid)
	require.Equal(t, "hash1", hash)

	// FTS finds it by a name token and by a stemmed body term ("race" -> "races").
	// (The hyphenated name tokenizes to chroma/boot/race under unicode61.)
	require.Equal(t, []string{m.ID}, ftsMatch(t, db, "chroma"))
	require.Contains(t, ftsMatch(t, db, "race"), m.ID)
}

func TestIndexMemoryUpsertRefreshesFTS(t *testing.T) {
	ix, db := newIndexer(t)
	ctx := context.Background()

	m := sampleMemory()
	m.FilePath = MemoryRelPath(m.Project, m.Name)
	require.NoError(t, ix.IndexMemory(ctx, m))
	require.Contains(t, ftsMatch(t, db, "readiness"), m.ID) // description has "readiness gate"

	// Change description; re-index. Old term must no longer match; new one must.
	m.Description = "supersession semantics changed"
	require.NoError(t, ix.IndexMemory(ctx, m))
	require.Empty(t, ftsMatch(t, db, "readiness"))
	require.Contains(t, ftsMatch(t, db, "supersession"), m.ID)

	// Exactly one index row and one fts row for this id (no duplication).
	var n int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories_index WHERE id=?`, m.ID).Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fts WHERE item_id=?`, m.ID).Scan(&n))
	require.Equal(t, 1, n)
}

func TestIndexMemoryInvalidAtStored(t *testing.T) {
	ix, db := newIndexer(t)
	ctx := context.Background()

	m := sampleMemory()
	m.FilePath = MemoryRelPath(m.Project, m.Name)
	invalid := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	m.InvalidAt = &invalid
	m.SupersededBy = "01K0REPLACEMENT0000000000B"
	require.NoError(t, ix.IndexMemory(ctx, m))

	var invalidAt, supersededBy sql.NullString
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT invalid_at, superseded_by FROM memories_index WHERE id=?`, m.ID).
		Scan(&invalidAt, &supersededBy))
	require.True(t, invalidAt.Valid)
	require.Equal(t, core.FormatTime(invalid), invalidAt.String)
	require.Equal(t, "01K0REPLACEMENT0000000000B", supersededBy.String)
}

func TestIndexNoteAndUnifiedSearch(t *testing.T) {
	ix, db := newIndexer(t)
	ctx := context.Background()

	m := sampleMemory()
	m.FilePath = MemoryRelPath(m.Project, m.Name)
	require.NoError(t, ix.IndexMemory(ctx, m))

	n := core.Note{
		ID:       "01K0NOTE00000000000000000A",
		Title:    "Chroma outage postmortem",
		Slug:     "chroma-outage",
		Project:  "seam",
		Body:     "the boot race caused a cascade\n",
		Created:  time.Now().UTC(),
		Updated:  time.Now().UTC(),
		FilePath: NoteRelPath("seam", "chroma-outage"),
	}
	require.NoError(t, ix.IndexNote(ctx, n))

	// A shared term ("boot") matches both the memory and the note.
	got := ftsMatch(t, db, "boot")
	require.Contains(t, got, m.ID)
	require.Contains(t, got, n.ID)

	// kind is retrievable to disambiguate the unified index.
	var kind string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT kind FROM fts WHERE item_id=?`, n.ID).Scan(&kind))
	require.Equal(t, kindNote, kind)
}

func TestDeleteByFilePath(t *testing.T) {
	ix, db := newIndexer(t)
	ctx := context.Background()

	m := sampleMemory()
	m.FilePath = MemoryRelPath(m.Project, m.Name)
	require.NoError(t, ix.IndexMemory(ctx, m))

	require.NoError(t, ix.DeleteByFilePath(ctx, m.FilePath))

	var n int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories_index WHERE id=?`, m.ID).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fts WHERE item_id=?`, m.ID).Scan(&n))
	require.Zero(t, n)

	// Deleting an absent path is a no-op.
	require.NoError(t, ix.DeleteByFilePath(ctx, m.FilePath))
}

func TestContentHashByFilePath(t *testing.T) {
	ix, _ := newIndexer(t)
	ctx := context.Background()

	_, found, err := ix.ContentHashByFilePath(ctx, "memory/seam/absent.md")
	require.NoError(t, err)
	require.False(t, found)

	m := sampleMemory()
	m.FilePath = MemoryRelPath(m.Project, m.Name)
	m.ContentHash = "deadbeef"
	require.NoError(t, ix.IndexMemory(ctx, m))

	hash, found, err := ix.ContentHashByFilePath(ctx, m.FilePath)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "deadbeef", hash)
}

func TestIndexMemoryRequiresID(t *testing.T) {
	ix, _ := newIndexer(t)
	m := sampleMemory()
	m.ID = ""
	require.Error(t, ix.IndexMemory(context.Background(), m))
}

func TestTagsJSONRoundTrip(t *testing.T) {
	s, err := tagsJSON([]string{"a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, parseTags(s))

	empty, err := tagsJSON(nil)
	require.NoError(t, err)
	require.Equal(t, "[]", empty)
	require.Nil(t, parseTags(empty))
}
