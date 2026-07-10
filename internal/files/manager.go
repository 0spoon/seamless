package files

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// defaultDebounce is how long a file must be quiet before the watcher re-indexes
// it. Editors emit several writes per save; this coalesces them.
const defaultDebounce = 300 * time.Millisecond

// Manager is the running files subsystem: it owns the filesystem Store and the
// SQLite Indexer, reconciles the trees against the index at startup, and watches
// them for out-of-band edits. Application writes go through it so their own
// writes are suppressed in the watcher (no re-index loop).
type Manager struct {
	store   *Store
	indexer *Indexer
	watcher *watcher
	logger  *slog.Logger
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
		logger:  logger,
	}
	w, err := newWatcher(dataDir, m.handleFile, defaultDebounce, logger)
	if err != nil {
		return nil, fmt.Errorf("files.NewManager: %w", err)
	}
	m.watcher = w
	return m, nil
}

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
	go func() {
		if err := m.watcher.run(ctx); err != nil && ctx.Err() == nil {
			m.logger.Error("files.Manager: watcher stopped", "error", err)
		}
	}()
	return nil
}

// Close stops the watcher.
func (m *Manager) Close() error { return m.watcher.close() }

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
		return m.indexer.IndexMemory(ctx, mem)
	case notesTree:
		note, err := ParseNote(content, relPath)
		if err != nil {
			return fmt.Errorf("files.handleFile: %w", err)
		}
		if note.ID == "" {
			m.logger.Warn("files.handleFile: note file has no id, skipping", "file_path", relPath)
			return nil
		}
		return m.indexer.IndexNote(ctx, note)
	default:
		return nil
	}
}

// WriteMemory writes a memory through the Store (suppressing the watcher's view
// of its own write) and indexes it synchronously. It returns the stored memory
// with FilePath and ContentHash populated.
func (m *Manager) WriteMemory(ctx context.Context, mem core.Memory) (core.Memory, error) {
	m.suppress(MemoryRelPath(mem.Project, mem.Name))
	written, err := m.store.WriteMemory(mem)
	if err != nil {
		return core.Memory{}, err
	}
	if err := m.indexer.IndexMemory(ctx, written); err != nil {
		return core.Memory{}, err
	}
	return written, nil
}

// WriteNote writes a note through the Store and indexes it synchronously.
func (m *Manager) WriteNote(ctx context.Context, note core.Note) (core.Note, error) {
	m.suppress(NoteRelPath(note.Project, note.Slug))
	written, err := m.store.WriteNote(note)
	if err != nil {
		return core.Note{}, err
	}
	if err := m.indexer.IndexNote(ctx, written); err != nil {
		return core.Note{}, err
	}
	return written, nil
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
