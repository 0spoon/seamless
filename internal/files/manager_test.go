package files

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

func newManager(t *testing.T) (*Manager, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	m, err := NewManager(dir, db, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	return m, db
}

func indexedCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n))
	return n
}

// Reconcile indexes files placed on disk out of band (as the importer does).
func TestReconcileIndexesDiskFiles(t *testing.T) {
	m, db := newManager(t)
	ctx := context.Background()

	// Write two memories and a note straight to disk (no indexing yet).
	mem := sampleMemory()
	_, err := m.store.WriteMemory(mem)
	require.NoError(t, err)
	mem2 := sampleMemory()
	mem2.ID = "01K0MEMORY000000000000000B"
	mem2.Name = "second-memory"
	_, err = m.store.WriteMemory(mem2)
	require.NoError(t, err)
	_, err = m.store.WriteNote(core.Note{
		ID: "01K0NOTE00000000000000000A", Title: "N", Slug: "n", Project: "seam",
		Body: "note body\n", Created: time.Now().UTC(), Updated: time.Now().UTC(),
	})
	require.NoError(t, err)

	require.Equal(t, 0, indexedCount(t, db, "memories_index"))

	require.NoError(t, m.Reconcile(ctx))
	require.Equal(t, 2, indexedCount(t, db, "memories_index"))
	require.Equal(t, 1, indexedCount(t, db, "notes_index"))
	require.Equal(t, 3, indexedCount(t, db, "fts"))
}

// legacyInboxNote writes a valid note file straight into the pre-rename
// notes/inbox directory, bypassing WriteNote (which now targets notes/_global).
func legacyInboxNote(t *testing.T, m *Manager, id, slug string) {
	t.Helper()
	content, err := RenderNote(core.Note{
		ID: id, Title: slug, Slug: slug, Body: "legacy body\n",
		Created: time.Now().UTC(), Updated: time.Now().UTC(),
	})
	require.NoError(t, err)
	dir := filepath.Join(m.store.DataDir(), notesTree, notesLegacyGlobalDir)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, slug+".md"), []byte(content), 0o644))
}

// A project-less note left in the legacy notes/inbox directory is relocated to
// notes/_global, and the following Reconcile indexes it at the new path -- no
// orphan, because a note's project comes from frontmatter, not the directory.
func TestMigrateNotesInboxToGlobal(t *testing.T) {
	m, db := newManager(t)
	legacyInboxNote(t, m, "01K0NOTE00000000000000000A", "stray")

	require.NoError(t, m.migrateNotesInboxToGlobal())

	oldPath := filepath.Join(m.store.DataDir(), notesTree, notesLegacyGlobalDir)
	require.NoDirExists(t, oldPath, "legacy notes/inbox must be gone after a clean move")
	require.True(t, m.store.Exists("notes/_global/stray.md"))

	require.NoError(t, m.Reconcile(context.Background()))
	require.Equal(t, 1, indexedCount(t, db, "notes_index"))
	var path string
	require.NoError(t, db.QueryRow(`SELECT file_path FROM notes_index`).Scan(&path))
	require.Equal(t, "notes/_global/stray.md", path, "index must point at the new path")
}

// Idempotent: with nothing in notes/inbox (the state after a first run or on a
// fresh data dir) the migration does nothing and does not error.
func TestMigrateNotesInboxToGlobal_NoLegacyDir(t *testing.T) {
	m, _ := newManager(t)
	require.NoError(t, m.migrateNotesInboxToGlobal())
	require.NoError(t, m.migrateNotesInboxToGlobal())
}

// Both directories exist and a slug is present in each: the migration merges the
// non-conflicting file, refuses to clobber the conflicting one, and keeps the
// legacy directory because it did not fully drain.
func TestMigrateNotesInboxToGlobal_MergeWithConflict(t *testing.T) {
	m, _ := newManager(t)
	// _global already owns "dup"; inbox holds "dup" (conflict) and "solo" (clean).
	_, err := m.store.WriteNote(core.Note{
		ID: "01K0NOTE0000000000000GLOBL", Title: "dup", Slug: "dup",
		Body: "global copy\n", Created: time.Now().UTC(), Updated: time.Now().UTC(),
	})
	require.NoError(t, err)
	globalDup := filepath.Join(m.store.DataDir(), notesTree, notesGlobalDir, "dup.md")
	before, err := os.ReadFile(globalDup)
	require.NoError(t, err)
	legacyInboxNote(t, m, "01K0NOTE00000000000000DUPB", "dup")
	legacyInboxNote(t, m, "01K0NOTE0000000000000SOLOA", "solo")

	require.NoError(t, m.migrateNotesInboxToGlobal())

	require.True(t, m.store.Exists("notes/_global/solo.md"), "the clean file moves")
	after, err := os.ReadFile(globalDup)
	require.NoError(t, err)
	require.Equal(t, before, after, "the conflicting global note is not clobbered")
	require.True(t, m.store.Exists("notes/inbox/dup.md"), "the conflicting legacy file is left in place")
	require.DirExists(t, filepath.Join(m.store.DataDir(), notesTree, notesLegacyGlobalDir),
		"a directory that did not fully drain is kept, not silently removed")
}

// A second Reconcile with no disk changes must be a no-op (content-hash skip).
func TestReconcileIsIdempotent(t *testing.T) {
	m, db := newManager(t)
	ctx := context.Background()
	_, err := m.store.WriteMemory(sampleMemory())
	require.NoError(t, err)

	require.NoError(t, m.Reconcile(ctx))
	var hash1 string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT content_hash FROM memories_index`).Scan(&hash1))

	require.NoError(t, m.Reconcile(ctx))
	require.Equal(t, 1, indexedCount(t, db, "memories_index"))
}

// Reconcile drops index rows whose file has been deleted from disk.
func TestReconcileRemovesOrphans(t *testing.T) {
	m, db := newManager(t)
	ctx := context.Background()
	written, err := m.store.WriteMemory(sampleMemory())
	require.NoError(t, err)
	require.NoError(t, m.Reconcile(ctx))
	require.Equal(t, 1, indexedCount(t, db, "memories_index"))

	require.NoError(t, m.store.Remove(written.FilePath))
	require.NoError(t, m.Reconcile(ctx))
	require.Equal(t, 0, indexedCount(t, db, "memories_index"))
	require.Equal(t, 0, indexedCount(t, db, "fts"))
}

// Reconcile picks up an edit to an existing file (hash changed => re-index).
func TestReconcileUpdatesChangedFile(t *testing.T) {
	m, db := newManager(t)
	ctx := context.Background()
	mem := sampleMemory()
	_, err := m.store.WriteMemory(mem)
	require.NoError(t, err)
	require.NoError(t, m.Reconcile(ctx))

	mem.Description = "edited description via disk"
	_, err = m.store.WriteMemory(mem)
	require.NoError(t, err)
	require.NoError(t, m.Reconcile(ctx))

	var desc string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT description FROM memories_index`).Scan(&desc))
	require.Equal(t, "edited description via disk", desc)
}

// The write-through path indexes immediately without a reconcile.
func TestManagerWriteMemoryIndexes(t *testing.T) {
	m, db := newManager(t)
	ctx := context.Background()
	_, err := m.WriteMemory(ctx, sampleMemory())
	require.NoError(t, err)
	require.Equal(t, 1, indexedCount(t, db, "memories_index"))
	require.Equal(t, 1, indexedCount(t, db, "fts"))
}

// Acceptance: editing a memory file on disk round-trips through the watcher into
// the index. Uses require.Eventually (no fixed sleeps).
func TestWatcherRoundTrip(t *testing.T) {
	m, db := newManager(t)
	m.watcher.debounce = 15 * time.Millisecond // speed up the test
	ctx := t.Context()
	require.NoError(t, m.Start(ctx))

	// Simulate an external editor writing a memory file into the tree.
	mem := sampleMemory()
	content, err := RenderMemory(mem)
	require.NoError(t, err)
	relPath := MemoryRelPath(mem.Project, mem.Name)
	abs := filepath.Join(m.store.DataDir(), filepath.FromSlash(relPath))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))

	require.Eventually(t, func() bool {
		return indexedCount(t, db, "memories_index") == 1
	}, 3*time.Second, 10*time.Millisecond, "watcher should index the new file")

	// Editing the file updates the index.
	mem.Description = "updated by editor"
	content, err = RenderMemory(mem)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
	require.Eventually(t, func() bool {
		var desc string
		if err := db.QueryRowContext(ctx, `SELECT description FROM memories_index`).Scan(&desc); err != nil {
			return false
		}
		return desc == "updated by editor"
	}, 3*time.Second, 10*time.Millisecond, "watcher should re-index the edit")

	// Deleting the file removes it from the index.
	require.NoError(t, os.Remove(abs))
	require.Eventually(t, func() bool {
		return indexedCount(t, db, "memories_index") == 0
	}, 3*time.Second, 10*time.Millisecond, "watcher should drop the deleted file")
}

// MoveMemory relocates the file and repoints the index row by id: after the
// move the old path is gone (disk and index) and the row carries the new
// project and path.
func TestMoveMemory_WriteThenRemove(t *testing.T) {
	m, db := newManager(t)
	ctx := context.Background()
	mem, err := m.WriteMemory(ctx, sampleMemory())
	require.NoError(t, err)
	oldPath := mem.FilePath

	moved, err := m.MoveMemory(ctx, mem, "otherproj")
	require.NoError(t, err)
	require.Equal(t, mem.ID, moved.ID, "a move keeps the ULID")
	require.Equal(t, "otherproj", moved.Project)
	require.NotEqual(t, oldPath, moved.FilePath)

	require.False(t, m.store.Exists(oldPath), "old file must be removed")
	require.True(t, m.store.Exists(moved.FilePath), "new file must exist")
	require.Equal(t, 1, indexedCount(t, db, "memories_index"))
	var project, filePath string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT project, file_path FROM memories_index WHERE id = ?`, mem.ID).Scan(&project, &filePath))
	require.Equal(t, "otherproj", project)
	require.Equal(t, moved.FilePath, filePath)
}

// A move onto a path owned by a different memory (same name in the target
// project) is refused before anything is written or removed: both memories
// keep their files and index rows.
func TestMoveMemory_TargetOccupiedRefused(t *testing.T) {
	m, db := newManager(t)
	ctx := context.Background()
	src, err := m.WriteMemory(ctx, sampleMemory())
	require.NoError(t, err)

	occupant := sampleMemory()
	occupant.ID = "01K0MEMORY000000000000000B"
	occupant.Project = "otherproj"
	occupant.Description = "the occupant of the target path"
	occupant, err = m.WriteMemory(ctx, occupant)
	require.NoError(t, err)

	_, err = m.MoveMemory(ctx, src, "otherproj")
	require.ErrorIs(t, err, ErrPathOccupied)

	// The source is untouched at its old path, the occupant keeps its content.
	require.True(t, m.store.Exists(src.FilePath), "source file must survive a refused move")
	got, err := m.store.ReadMemory(occupant.FilePath)
	require.NoError(t, err)
	require.Equal(t, occupant.ID, got.ID)
	require.Equal(t, "the occupant of the target path", got.Description)
	require.Equal(t, 2, indexedCount(t, db, "memories_index"))
}

// Re-creating the name of a superseded memory must not clobber its tombstone
// file: the write is refused until the name is freed (memory_delete), keeping
// the supersession history readable.
func TestWriteMemory_ReviveRefusedWhileTombstoneHolds(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()

	// Write the memory, then mark it superseded in place (as lifecycle.Supersede
	// does: same id, invalid_at + superseded_by stamped, tombstone in the body).
	old, err := m.WriteMemory(ctx, sampleMemory())
	require.NoError(t, err)
	at := time.Now().UTC()
	old.InvalidAt = &at
	old.SupersededBy = "01K0MEMORY000000000000000B"
	old.Body = old.Body + "\n> Superseded by replacement on 2026-07-14\n"
	old, err = m.WriteMemory(ctx, old) // same id: the in-place stamp is allowed
	require.NoError(t, err)

	// A revive -- same project+name, fresh ULID -- must be refused.
	revive := sampleMemory()
	revive.ID = "01K0MEMORY000000000000000C"
	revive.Body = "brand new body that would clobber the tombstone\n"
	_, err = m.WriteMemory(ctx, revive)
	require.ErrorIs(t, err, ErrPathOccupied)

	// The tombstone file is intact and still readable.
	got, err := m.store.ReadMemory(old.FilePath)
	require.NoError(t, err)
	require.Equal(t, old.ID, got.ID)
	require.Contains(t, got.Body, "Superseded by replacement")

	// Deleting the tombstone frees the name; the revive then succeeds.
	require.NoError(t, m.Remove(ctx, old.FilePath))
	written, err := m.WriteMemory(ctx, revive)
	require.NoError(t, err)
	require.Equal(t, revive.ID, written.ID)
}

// A note write onto a path owned by a different note (slug collision) is
// refused rather than clobbering the existing file.
func TestWriteNote_SlugCollisionRefused(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	now := time.Now().UTC()
	first, err := m.WriteNote(ctx, core.Note{
		ID: "01K0NOTE00000000000000000A", Title: "N", Slug: "n", Project: "seam",
		Body: "first body\n", Created: now, Updated: now,
	})
	require.NoError(t, err)

	_, err = m.WriteNote(ctx, core.Note{
		ID: "01K0NOTE00000000000000000B", Title: "N", Slug: "n", Project: "seam",
		Body: "second body\n", Created: now, Updated: now,
	})
	require.ErrorIs(t, err, ErrPathOccupied)

	got, err := m.store.ReadNote(first.FilePath)
	require.NoError(t, err)
	require.Equal(t, first.ID, got.ID)
	require.Equal(t, "first body\n", got.Body)
}

// An on-disk file that is not indexed yet (out-of-band write) still occupies
// its path: the files on disk are the source of truth.
func TestWriteMemory_UnindexedDiskFileOccupies(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	_, err := m.store.WriteMemory(sampleMemory()) // straight to disk, no index row
	require.NoError(t, err)

	clash := sampleMemory()
	clash.ID = "01K0MEMORY000000000000000B"
	_, err = m.WriteMemory(ctx, clash)
	require.ErrorIs(t, err, ErrPathOccupied)
}

// A write through the Manager must not trigger a watcher re-index (suppression).
func TestManagerWriteSuppressesWatcher(t *testing.T) {
	m, _ := newManager(t)
	m.watcher.debounce = 15 * time.Millisecond
	ctx := t.Context()
	require.NoError(t, m.Start(ctx))

	mem := sampleMemory()
	relPath := MemoryRelPath(mem.Project, mem.Name)
	abs, err := m.store.abs(relPath)
	require.NoError(t, err)

	// The suppression entry must exist right after a managed write.
	_, err = m.WriteMemory(ctx, mem)
	require.NoError(t, err)
	require.True(t, m.watcher.suppressedNow(abs), "managed write should suppress the watcher")
}
