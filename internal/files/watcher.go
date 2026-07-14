package files

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// changeHandler is called with a data-dir-relative path after a debounced file
// change (or immediately on remove).
type changeHandler func(ctx context.Context, relPath string) error

// suppressWindow is how long a self-write suppression entry lives. A single
// application write emits several fsnotify events; all are suppressed within it.
const suppressWindow = 2 * time.Second

// watcher watches the memory/ and notes/ trees under a data dir and calls a
// handler with a data-dir-relative path on each change. Ported from Seam v1
// internal/watcher, reduced to a single owner and two fixed trees.
type watcher struct {
	fsw      *fsnotify.Watcher
	dataDir  string
	handler  changeHandler
	debounce time.Duration
	logger   *slog.Logger

	mu         sync.Mutex
	suppressed map[string]time.Time   // absolute path -> expiry
	pending    map[string]*time.Timer // absolute path -> debounce timer
	gen        map[string]uint64      // absolute path -> debounce generation
	closed     bool                   // set by close(); no debounce handler starts after it

	// inflight tracks debounce handler invocations so close() can drain them:
	// the callback only Adds under mu with closed still false, and close() Waits
	// after setting closed, so a caller tearing down (watcher first, DB second)
	// never races an in-flight re-index.
	inflight sync.WaitGroup

	ctx    context.Context
	cancel context.CancelFunc
}

func newWatcher(dataDir string, handler changeHandler, debounce time.Duration, logger *slog.Logger) (*watcher, error) {
	if handler == nil {
		return nil, fmt.Errorf("files.newWatcher: handler must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("files.newWatcher: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &watcher{
		fsw:        fsw,
		dataDir:    dataDir,
		handler:    handler,
		debounce:   debounce,
		logger:     logger,
		suppressed: make(map[string]time.Time),
		pending:    make(map[string]*time.Timer),
		gen:        make(map[string]uint64),
		ctx:        ctx,
		cancel:     cancel,
	}, nil
}

// watchTree recursively adds every directory under root to the fsnotify watch
// list. A missing root is not an error (the tree may not exist yet).
//
// It holds no lock: w.mu guards the suppressed/pending/gen maps, none of which
// this touches, and fsnotify's Add is internally synchronized -- which is why
// handleEvent already Adds a newly created directory without w.mu. Taking it
// here would only mean holding a mutex across a full filesystem walk.
func (w *watcher) watchTree(root string) error {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return filepath.SkipDir // never follow symlinks out of the tree
		}
		if err := w.fsw.Add(path); err != nil {
			return fmt.Errorf("files.watchTree: add %s: %w", path, err)
		}
		return nil
	})
}

// ignoreNext suppresses events for absPath for the suppression window, so a
// write performed by the application does not trigger a re-index of its own file.
func (w *watcher) ignoreNext(absPath string) {
	abs, err := filepath.Abs(absPath)
	if err != nil {
		return
	}
	w.mu.Lock()
	w.suppressed[abs] = time.Now().Add(suppressWindow)
	w.mu.Unlock()
}

// run processes events until ctx (or the watcher) is cancelled.
func (w *watcher) run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.ctx.Done():
			return nil
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			w.handleEvent(ctx, ev)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			w.logger.Error("files.watcher: fsnotify error", "error", err)
		}
	}
}

// close stops the watcher: no debounce handler starts after it returns, and any
// handler already running is drained first, so the caller can safely tear down
// what the handler touches (the DB) right after. Idempotent.
func (w *watcher) close() error {
	w.cancel()
	w.mu.Lock()
	w.closed = true
	for _, t := range w.pending {
		t.Stop()
	}
	w.pending = make(map[string]*time.Timer)
	clear(w.gen) // a stopped timer never fires; without this a mid-flight one would still match
	w.mu.Unlock()
	w.inflight.Wait()
	return w.fsw.Close()
}

func (w *watcher) handleEvent(ctx context.Context, ev fsnotify.Event) {
	abs := ev.Name

	// Newly created directory: start watching it so files created inside are
	// seen, then re-scan it for files created in the race window between the
	// directory appearing and the watch being armed.
	if ev.Has(fsnotify.Create) {
		if info, err := os.Lstat(abs); err == nil && info.IsDir() {
			if err := w.fsw.Add(abs); err != nil {
				w.logger.Warn("files.watcher: add new dir", "path", abs, "error", err)
			}
			w.rescanDir(ctx, abs)
			return
		}
	}

	if !strings.HasSuffix(abs, ".md") {
		return
	}
	if w.suppressedNow(abs) {
		return
	}

	relPath, err := filepath.Rel(w.dataDir, abs)
	if err != nil {
		return
	}
	relPath = filepath.ToSlash(relPath)
	if _, _, ok := treeAndRel(relPath); !ok {
		return
	}

	// Removes fire immediately; create/write/rename are debounced.
	if ev.Has(fsnotify.Remove) {
		w.fire(ctx, relPath)
		return
	}
	if ev.Has(fsnotify.Create) || ev.Has(fsnotify.Write) || ev.Has(fsnotify.Rename) {
		w.debounceFire(ctx, abs, relPath)
	}
}

// suppressedNow reports whether abs is currently suppressed, cleaning expired
// entries along the way.
func (w *watcher) suppressedNow(abs string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	for p, exp := range w.suppressed {
		if now.After(exp) {
			delete(w.suppressed, p)
		}
	}
	exp, ok := w.suppressed[abs]
	return ok && now.Before(exp)
}

// debounceFire (re)arms a timer for abs; the handler fires once the file has been
// quiet for the debounce interval. A generation counter defeats stale timers.
func (w *watcher) debounceFire(ctx context.Context, abs, relPath string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return // shutting down: never arm a timer close() cannot see
	}

	if t, ok := w.pending[abs]; ok {
		t.Stop()
	}
	w.gen[abs]++
	gen := w.gen[abs]

	w.pending[abs] = time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		// closed: the watcher is shutting down -- a timer that already fired and
		// was waiting on the lock while close() ran must not start a handler now
		// (its target, e.g. the DB, may be about to close). Checked under the same
		// lock that close() sets it under, and the inflight Add happens here too,
		// so close()'s Wait cannot miss a handler that got past this check.
		if w.closed || w.gen[abs] != gen {
			w.mu.Unlock()
			return
		}
		delete(w.pending, abs)
		delete(w.gen, abs)
		w.inflight.Add(1)
		w.mu.Unlock()
		defer w.inflight.Done()

		if ctx.Err() != nil {
			return
		}
		w.fire(ctx, relPath)
	})
}

func (w *watcher) fire(ctx context.Context, relPath string) {
	if err := w.handler(ctx, relPath); err != nil {
		w.logger.Error("files.watcher: handler error", "rel_path", relPath, "error", err)
	}
}

// rescanDir fires the handler for every .md file directly under (or below) a
// newly created directory. The handler is idempotent (content-hash skip), so
// double-firing with the live watch is harmless.
func (w *watcher) rescanDir(ctx context.Context, dir string) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil //nolint:nilerr // best-effort catch-up scan
		}
		rel, err := filepath.Rel(w.dataDir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if _, _, ok := treeAndRel(rel); ok {
			w.fire(ctx, rel)
		}
		return nil
	})
}
