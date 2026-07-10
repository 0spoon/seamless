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
