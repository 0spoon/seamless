package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/llm"
	"github.com/0spoon/seamless/internal/store"
)

// ErrPathOccupied is returned when a write would land on a file owned by a
// different item (by id). In particular a superseded or archived memory keeps
// its tombstone file at memory/{project}/{name}.md, so that name stays occupied
// until the tombstone is deleted; a new memory silently overwriting it would
// destroy readable supersession history. Callers free the name (memory_delete)
// or pick another one.
var ErrPathOccupied = errors.New("target path belongs to a different item")

const (
	// defaultDebounce is how long a file must be quiet before the watcher
	// re-indexes it. Editors emit several writes per save; this coalesces them.
	defaultDebounce = 300 * time.Millisecond
	// maxEmbedRunes bounds the text sent to the embedder (roughly a few thousand
	// tokens). One vector per item; long bodies are truncated, not chunked.
	maxEmbedRunes = 8000
)

// Manager is the running files subsystem: it owns the filesystem Store and the
// SQLite Indexer, reconciles the trees against the index at startup, and watches
// them for out-of-band edits. Application writes go through it so their own
// writes are suppressed in the watcher (no re-index loop). An optional embedder
// keeps the vector index in sync with the file content (best-effort).
type Manager struct {
	store    *Store
	indexer  *Indexer
	watcher  *watcher
	db       *sql.DB
	embedder llm.Embedder
	logger   *slog.Logger

	// runDone is closed when the watcher event loop goroutine exits; nil until
	// Start launches it. Close waits on it so no handler (which touches the DB)
	// outlives the Manager. Start and Close are called from the owner's goroutine
	// (the daemon's serve path), never concurrently.
	runDone chan struct{}
}

// NewManager builds a Manager over dataDir backed by db. It does not touch the
// filesystem or start watching until Start is called.
func NewManager(dataDir string, db *sql.DB, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		store:   NewStore(dataDir),
		indexer: NewIndexer(db),
		db:      db,
		logger:  logger,
	}
	w, err := newWatcher(dataDir, m.handleFile, defaultDebounce, logger)
	if err != nil {
		return nil, fmt.Errorf("files.NewManager: %w", err)
	}
	m.watcher = w
	return m, nil
}

// SetEmbedder enables vector indexing. When set, every (re)indexed item is
// embedded and its vector upserted; embedding failures are logged, not fatal, so
// a slow or down embedder never blocks a write or an edit. Nil disables it.
func (m *Manager) SetEmbedder(e llm.Embedder) { m.embedder = e }

// Store exposes the filesystem layer (read-only helpers for other packages).
func (m *Manager) Store() *Store { return m.store }

// Indexer exposes the index layer.
func (m *Manager) Indexer() *Indexer { return m.indexer }

// Start creates the tree directories, begins watching them, reconciles the index
// against disk, and launches the event loop in a background goroutine. The loop
// stops when ctx is cancelled or Close is called.
func (m *Manager) Start(ctx context.Context) error {
	for _, tree := range []string{memoryTree, notesTree} {
		dir := filepath.Join(m.store.DataDir(), tree)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("files.Start: mkdir %s: %w", dir, err)
		}
		if err := m.watcher.watchTree(dir); err != nil {
			return fmt.Errorf("files.Start: watch %s: %w", dir, err)
		}
	}
	if err := m.Reconcile(ctx); err != nil {
		return fmt.Errorf("files.Start: reconcile: %w", err)
	}
	m.runDone = make(chan struct{})
	go func() {
		defer close(m.runDone)
		if err := m.watcher.run(ctx); err != nil && ctx.Err() == nil {
			m.logger.Error("files.Manager: watcher stopped", "error", err)
		}
	}()
	return nil
}

// Close stops the watcher and drains its work: after Close returns, no debounce
// handler is running or will run, and the event-loop goroutine has exited -- so
// the caller may close the DB the handlers write to. Safe to call more than once,
// and without a prior Start.
func (m *Manager) Close() error {
	err := m.watcher.close()
	if m.runDone != nil {
		<-m.runDone
	}
	return err
}

// Reconcile brings the index into agreement with the trees on disk: it re-indexes
// changed/new files and drops index rows whose file has been deleted.
func (m *Manager) Reconcile(ctx context.Context) error {
	seen := make(map[string]bool)

	for _, tree := range []string{memoryTree, notesTree} {
		root := filepath.Join(m.store.DataDir(), tree)
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if d.Type()&os.ModeSymlink != 0 {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 || filepath.Ext(path) != ".md" {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			rel, err := filepath.Rel(m.store.DataDir(), path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			seen[rel] = true
			return m.handleFile(ctx, rel)
		})
		if err != nil {
			return fmt.Errorf("files.Reconcile: walk %s: %w", root, err)
		}
	}

	// Drop index rows whose file no longer exists on disk.
	indexed, err := m.indexer.AllFilePaths(ctx)
	if err != nil {
		return err
	}
	for _, rel := range indexed {
		if seen[rel] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.indexer.DeleteByFilePath(ctx, rel); err != nil {
			m.logger.Warn("files.Reconcile: delete orphan", "file_path", rel, "error", err)
		}
	}
	return nil
}

// handleFile is the watcher/reconciler callback: it (re)indexes a changed file or
// deletes the index row when the file is gone. Unchanged files (same content
// hash) are skipped, so re-indexing is idempotent and cheap.
func (m *Manager) handleFile(ctx context.Context, relPath string) error {
	tree, _, ok := treeAndRel(relPath)
	if !ok {
		return nil
	}
	if !m.store.Exists(relPath) {
		return m.indexer.DeleteByFilePath(ctx, relPath)
	}

	content, err := m.store.readFile(relPath)
	if err != nil {
		return fmt.Errorf("files.handleFile: read %s: %w", relPath, err)
	}
	newHash := ContentHash(content)
	if oldHash, found, err := m.indexer.ContentHashByFilePath(ctx, relPath); err != nil {
		return err
	} else if found && oldHash == newHash {
		return nil // unchanged
	}

	switch tree {
	case memoryTree:
		mem, err := ParseMemory(content, relPath)
		if err != nil {
			return fmt.Errorf("files.handleFile: %w", err)
		}
		if mem.ID == "" {
			m.logger.Warn("files.handleFile: memory file has no id, skipping", "file_path", relPath)
			return nil
		}
		if err := m.indexer.IndexMemory(ctx, mem); err != nil {
			return err
		}
		if !m.embedItem(ctx, kindMemory, mem.ID, memoryEmbedText(mem)) {
			m.clearHashForRetry(ctx, relPath)
		}
		return nil
	case notesTree:
		note, err := ParseNote(content, relPath)
		if err != nil {
			return fmt.Errorf("files.handleFile: %w", err)
		}
		if note.ID == "" {
			m.logger.Warn("files.handleFile: note file has no id, skipping", "file_path", relPath)
			return nil
		}
		if err := m.indexer.IndexNote(ctx, note); err != nil {
			return err
		}
		if !m.embedItem(ctx, kindNote, note.ID, noteEmbedText(note)) {
			m.clearHashForRetry(ctx, relPath)
		}
		return nil
	default:
		return nil
	}
}

// embedItem embeds text and upserts the vector for an item, reporting whether
// the item's vector is now in sync with its content. It is best-effort: any
// embedding/store failure is logged rather than propagated, so a down embedder
// never blocks indexing. A false return means the vector is missing or stale;
// the caller clears the recorded content hash so a later reconcile retries the
// embed. With no embedder configured there is nothing to retry, so it reports
// true.
func (m *Manager) embedItem(ctx context.Context, kind, id, text string) bool {
	if m.embedder == nil {
		return true
	}
	vec, err := m.embedder.Embed(ctx, text)
	if err != nil {
		m.logger.Warn("files: embed failed", "kind", kind, "id", id, "error", err)
		return false
	}
	if err := store.UpsertEmbedding(ctx, m.db, id, kind, m.embedder.Model(), vec); err != nil {
		m.logger.Warn("files: upsert embedding failed", "kind", kind, "id", id, "error", err)
		return false
	}
	return true
}

// clearHashForRetry blanks the indexed content hash after a failed embed, so
// the skip-unchanged check does not pin the item to a missing or stale vector:
// the next reconcile (or watcher event) sees the file as changed, re-indexes
// it, and retries the embed. Best-effort; the embed failure itself was already
// logged, so a failure here only adds its own warning.
func (m *Manager) clearHashForRetry(ctx context.Context, relPath string) {
	if err := m.indexer.ClearContentHash(ctx, relPath); err != nil {
		m.logger.Warn("files: clear content hash for embed retry", "file_path", relPath, "error", err)
	}
}

// memoryEmbedText is the text embedded for a memory: name and one-line
// description carry the most signal, followed by the body.
func memoryEmbedText(m core.Memory) string {
	return truncateRunes(m.Name+"\n"+m.Description+"\n\n"+m.Body, maxEmbedRunes)
}

// noteEmbedText is the text embedded for a note.
func noteEmbedText(n core.Note) string {
	return truncateRunes(n.Title+"\n"+n.Description+"\n\n"+n.Body, maxEmbedRunes)
}

// truncateRunes caps s at max runes (never splitting a multi-byte rune).
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// WriteMemory writes a memory through the Store (suppressing the watcher's view
// of its own write) and indexes it synchronously. It returns the stored memory
// with FilePath and ContentHash populated. A path already owned by a different
// memory -- notably the tombstone file of a superseded memory whose name the
// write would revive -- is refused with ErrPathOccupied rather than overwritten.
func (m *Manager) WriteMemory(ctx context.Context, mem core.Memory) (core.Memory, error) {
	relPath := MemoryRelPath(mem.Project, mem.Name)
	if err := m.ensurePathFree(ctx, relPath, mem.ID); err != nil {
		return core.Memory{}, fmt.Errorf("files.WriteMemory: %w", err)
	}
	m.suppress(relPath)
	written, err := m.store.WriteMemory(mem)
	if err != nil {
		return core.Memory{}, err
	}
	if err := m.indexer.IndexMemory(ctx, written); err != nil {
		return core.Memory{}, err
	}
	if !m.embedItem(ctx, kindMemory, written.ID, memoryEmbedText(written)) {
		m.clearHashForRetry(ctx, written.FilePath)
	}
	return written, nil
}

// MoveMemory relocates a memory to another project, keeping its ULID. It mirrors
// the fixed note-move recipe: refuse when a different memory already owns the
// target path (WriteMemory's occupancy guard), and write the new file BEFORE
// removing the old one -- the index row is keyed by id, so the write repoints
// its file_path and a failed write leaves the memory intact at its old path
// instead of deleting it outright. The memory keeps its name; inbound [[name]]
// wiki-links resolve globally by bare name, so a move needs no link rewrite. The
// caller is responsible for bumping Updated. It is a no-op when toProject already
// equals the memory's project (idempotent for a retried apply).
func (m *Manager) MoveMemory(ctx context.Context, mem core.Memory, toProject string) (core.Memory, error) {
	if mem.Project == toProject {
		return m.WriteMemory(ctx, mem) // already home; re-write refreshes the index
	}
	oldPath := MemoryRelPath(mem.Project, mem.Name)
	mem.Project = toProject
	written, err := m.WriteMemory(ctx, mem)
	if err != nil {
		return core.Memory{}, fmt.Errorf("files.MoveMemory: write new path: %w", err)
	}
	// The index row now points at the new path, so this only deletes the old file.
	if err := m.Remove(ctx, oldPath); err != nil {
		return core.Memory{}, fmt.Errorf("files.MoveMemory: drop old path: %w", err)
	}
	return written, nil
}

// WriteNote writes a note through the Store and indexes it synchronously. As
// with WriteMemory, a path owned by a different note (a slug collision) is
// refused with ErrPathOccupied rather than overwritten.
func (m *Manager) WriteNote(ctx context.Context, note core.Note) (core.Note, error) {
	relPath := NoteRelPath(note.Project, note.Slug)
	if err := m.ensurePathFree(ctx, relPath, note.ID); err != nil {
		return core.Note{}, fmt.Errorf("files.WriteNote: %w", err)
	}
	m.suppress(relPath)
	written, err := m.store.WriteNote(note)
	if err != nil {
		return core.Note{}, err
	}
	if err := m.indexer.IndexNote(ctx, written); err != nil {
		return core.Note{}, err
	}
	if !m.embedItem(ctx, kindNote, written.ID, noteEmbedText(written)) {
		m.clearHashForRetry(ctx, written.FilePath)
	}
	return written, nil
}

// ensurePathFree refuses a write to relPath when a different item (by id)
// already owns it, checking the index row first (file_path is UNIQUE, so a
// collision would fail the upsert after the file was already clobbered) and
// then the file on disk (the source of truth; it may exist unindexed after an
// out-of-band write). A file whose id cannot be established is treated as
// occupied: overwriting content of unknown ownership loses data.
func (m *Manager) ensurePathFree(ctx context.Context, relPath, id string) error {
	if ownerID, found, err := m.indexer.IDByFilePath(ctx, relPath); err != nil {
		return err
	} else if found && ownerID != id {
		return fmt.Errorf("%w: %s is held by %s", ErrPathOccupied, relPath, ownerID)
	}
	if !m.store.Exists(relPath) {
		return nil
	}
	tree, _, _ := treeAndRel(relPath)
	var ownerID string
	switch tree {
	case memoryTree:
		mem, err := m.store.ReadMemory(relPath)
		if err != nil {
			return fmt.Errorf("%w: %s exists but its owner is unreadable: %w", ErrPathOccupied, relPath, err)
		}
		ownerID = mem.ID
	case notesTree:
		note, err := m.store.ReadNote(relPath)
		if err != nil {
			return fmt.Errorf("%w: %s exists but its owner is unreadable: %w", ErrPathOccupied, relPath, err)
		}
		ownerID = note.ID
	}
	if ownerID != id {
		return fmt.Errorf("%w: %s is held by %q", ErrPathOccupied, relPath, ownerID)
	}
	return nil
}

// Remove deletes a memory/note file (suppressing the watcher) and its index row.
func (m *Manager) Remove(ctx context.Context, relPath string) error {
	m.suppress(relPath)
	if err := m.store.Remove(relPath); err != nil {
		return err
	}
	return m.indexer.DeleteByFilePath(ctx, relPath)
}

// suppress tells the watcher to ignore its own imminent write to relPath.
func (m *Manager) suppress(relPath string) {
	if m.watcher == nil {
		return
	}
	if abs, err := m.store.abs(relPath); err == nil {
		m.watcher.ignoreNext(abs)
	}
}
