package files

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/0spoon/seamless/internal/core"
)

// ErrNoChange is returned by a MutateMemory/MutateNote callback that decided the
// item is already in its desired state. The write is skipped and the loaded item
// is returned with a nil error, so an idempotent flip (favorite_set on an
// already-starred memory) neither rewrites the file nor re-indexes it. It is a
// control signal, never surfaced to a caller as a failure.
var ErrNoChange = errors.New("no change")

// Mutate serializes a read-modify-write over relPath: fn runs with the path's
// lock held, so no other Mutate on the same file can interleave between fn's
// read and its write.
//
// This is the app-side fix for the lost update. Every mutating handler used to
// read an item, edit the struct, and write the whole file back; two of them
// racing on one file both read the same starting content and the second rename
// won, erasing the first write with no error anywhere -- the index upsert is by
// id, so even the UNIQUE file_path constraint stayed quiet. seamlessd is a
// single daemon, which is what makes an in-process lock a complete answer rather
// than a partial one: every application write to the corpus goes through this
// Manager. (An out-of-band editor write is a different problem, handled by the
// expect_hash precondition, which compares against the FILE inside this lock.)
//
// The lock is NOT reentrant. fn must not call Mutate for a path it already
// holds, and the Manager's own WriteMemory/WriteNote/Remove deliberately take no
// lock, so calling them from inside fn -- which is the point -- cannot deadlock.
func (m *Manager) Mutate(ctx context.Context, relPath string, fn func(context.Context) error) error {
	return m.MutatePaths(ctx, []string{relPath}, fn)
}

// MutatePaths is Mutate over several paths at once, for a mutation that spans
// two files: a note moving to another project writes the new path and removes
// the old one, and both halves must be serialized against anything else
// touching either. Paths are locked in sorted order, which is what keeps two
// concurrent moves in opposite directions from deadlocking; duplicates collapse
// so passing the same path twice is safe rather than a self-deadlock.
func (m *Manager) MutatePaths(ctx context.Context, relPaths []string, fn func(context.Context) error) error {
	keys, err := m.lockKeys(relPaths)
	if err != nil {
		return err
	}
	release, err := m.locks.acquire(ctx, keys)
	if err != nil {
		return err
	}
	defer release()
	return fn(ctx)
}

// MutateMemory reads the memory at relPath, hands it to fn, and writes back what
// fn returns -- all under the path's lock. It is the typed shorthand for the
// common shape; reach for Mutate directly when the mutation is not one
// load-edit-store of a single memory (memory_write, which must handle the file
// not existing yet, is the notable case).
//
// fn returning ErrNoChange skips the write and reports the loaded memory.
func (m *Manager) MutateMemory(ctx context.Context, relPath string, fn func(context.Context, core.Memory) (core.Memory, error)) (core.Memory, error) {
	var out core.Memory
	err := m.Mutate(ctx, relPath, func(ctx context.Context) error {
		loaded, err := m.store.ReadMemory(relPath)
		if err != nil {
			return err
		}
		edited, err := fn(ctx, loaded)
		if errors.Is(err, ErrNoChange) {
			out = loaded
			return nil
		}
		if err != nil {
			return err
		}
		written, err := m.WriteMemory(ctx, edited)
		if err != nil {
			return err
		}
		out = written
		return nil
	})
	if err != nil {
		return core.Memory{}, err
	}
	return out, nil
}

// MutateNote is MutateMemory for notes: read, edit, write, all under the lock.
// fn returning ErrNoChange skips the write and reports the loaded note.
func (m *Manager) MutateNote(ctx context.Context, relPath string, fn func(context.Context, core.Note) (core.Note, error)) (core.Note, error) {
	var out core.Note
	err := m.Mutate(ctx, relPath, func(ctx context.Context) error {
		loaded, err := m.store.ReadNote(relPath)
		if err != nil {
			return err
		}
		edited, err := fn(ctx, loaded)
		if errors.Is(err, ErrNoChange) {
			out = loaded
			return nil
		}
		if err != nil {
			return err
		}
		written, err := m.WriteNote(ctx, edited)
		if err != nil {
			return err
		}
		out = written
		return nil
	})
	if err != nil {
		return core.Note{}, err
	}
	return out, nil
}

// lockKeys normalizes each path to its lock key and returns them sorted and
// deduplicated. Normalization matters: two spellings of one file ("memory/p/x.md"
// and "memory/p//x.md") must map to the same key or they would take different
// locks and race anyway. It reuses cleanRel, so a path that escapes the data dir
// is refused here rather than inside fn -- the same boundary every other file
// operation applies, just earlier.
func (m *Manager) lockKeys(relPaths []string) ([]string, error) {
	if len(relPaths) == 0 {
		return nil, errors.New("files.Mutate: no path given")
	}
	keys := make([]string, 0, len(relPaths))
	for _, p := range relPaths {
		clean, err := m.store.cleanRel(p)
		if err != nil {
			return nil, fmt.Errorf("files.Mutate: %s: %w", p, err)
		}
		keys = append(keys, clean)
	}
	slices.Sort(keys)
	return slices.Compact(keys), nil
}

// pathLocks is a keyed mutex table: one lock per corpus file, created on demand
// and dropped once nothing holds or awaits it, so a long-lived daemon's table
// stays proportional to in-flight mutations rather than to the corpus.
type pathLocks struct {
	mu    sync.Mutex
	locks map[string]*pathLock
}

// pathLock is one file's lock. The semaphore is a 1-buffered channel rather than
// a sync.Mutex so a waiter can honor context cancellation: a mutation blocked
// behind a slow one must fail with the request that was abandoned, not pin a
// goroutine to a dead client.
type pathLock struct {
	sem  chan struct{}
	refs int
}

// acquire takes every key's lock, in the order given (callers pass sorted keys,
// which is the deadlock discipline), and returns the matching release. On
// cancellation it unwinds whatever it already holds and returns ctx's error, so
// a failed acquire leaves nothing locked.
func (p *pathLocks) acquire(ctx context.Context, keys []string) (func(), error) {
	held := make([]string, 0, len(keys))
	release := func() {
		for i := len(held) - 1; i >= 0; i-- {
			p.unlock(held[i])
		}
	}
	for _, k := range keys {
		l := p.ref(k)
		select {
		case l.sem <- struct{}{}:
			held = append(held, k)
		case <-ctx.Done():
			p.deref(k)
			release()
			return nil, ctx.Err()
		}
	}
	return release, nil
}

// ref returns the lock for key, creating it if needed, and counts this caller as
// interested in it so a concurrent unlock cannot delete it out from under them.
func (p *pathLocks) ref(key string) *pathLock {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.locks == nil {
		p.locks = make(map[string]*pathLock)
	}
	l := p.locks[key]
	if l == nil {
		l = &pathLock{sem: make(chan struct{}, 1)}
		p.locks[key] = l
	}
	l.refs++
	return l
}

// deref drops an interest taken by ref without having held the semaphore (the
// cancelled-while-waiting path).
func (p *pathLocks) deref(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l := p.locks[key]; l != nil {
		l.refs--
		if l.refs == 0 {
			delete(p.locks, key)
		}
	}
}

// unlock releases the semaphore and drops the interest taken by ref.
func (p *pathLocks) unlock(key string) {
	p.mu.Lock()
	l := p.locks[key]
	if l == nil {
		p.mu.Unlock()
		return
	}
	l.refs--
	if l.refs == 0 {
		delete(p.locks, key)
	}
	p.mu.Unlock()
	<-l.sem
}
