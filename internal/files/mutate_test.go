package files

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// bodyLines splits a stored body into its non-blank lines. Render/parse
// normalizes the trailing newline, so a plain Split would count phantom empties.
func bodyLines(body string) []string {
	var out []string
	for line := range strings.SplitSeq(body, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// seedMemory writes a memory through the manager and returns it.
func seedMemory(t *testing.T, m *Manager, name, body string) core.Memory {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	now := time.Now().UTC()
	written, err := m.WriteMemory(context.Background(), core.Memory{
		ID: id, Kind: core.KindGotcha, Name: name, Description: "seeded",
		Project: "demo", Body: body, Created: now, Updated: now, ValidFrom: now,
	})
	require.NoError(t, err)
	return written
}

// The lost update this whole substrate exists to remove: two appenders racing on
// one memory. Without the lock both read the same starting body and the second
// rename wins, so one append vanishes with no error anywhere.
func TestMutateMemory_ConcurrentAppendersLoseNothing(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	seeded := seedMemory(t, m, "raced", "start")

	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Go(func() {
			_, errs[i] = m.MutateMemory(ctx, seeded.FilePath, func(_ context.Context, mem core.Memory) (core.Memory, error) {
				mem.Body = strings.TrimRight(mem.Body, "\n") + fmt.Sprintf("\nline-%d", i)
				return mem, nil
			})
		})
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "writer %d", i)
	}

	final, err := m.Store().ReadMemory(seeded.FilePath)
	require.NoError(t, err)
	for i := range writers {
		require.Contains(t, final.Body, fmt.Sprintf("line-%d", i),
			"every serialized append must survive; body was:\n%s", final.Body)
	}
}

// The same guarantee for notes, and through the generic Mutate rather than the
// typed helper -- the console favorites and gardener paths use that shape.
func TestMutate_SerializesGenericReadModifyWrite(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	id, err := core.NewID()
	require.NoError(t, err)
	now := time.Now().UTC()
	seeded, err := m.WriteNote(ctx, core.Note{
		ID: id, Title: "Raced", Slug: "raced", Project: "demo", Body: "0",
		Created: now, Updated: now,
	})
	require.NoError(t, err)

	const writers = 8
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			require.NoError(t, m.Mutate(ctx, seeded.FilePath, func(ctx context.Context) error {
				note, err := m.Store().ReadNote(seeded.FilePath)
				if err != nil {
					return err
				}
				// Each writer numbers its line from what it read, so a lost
				// update shows up as a duplicate number, not just a short body.
				n := len(bodyLines(note.Body))
				note.Body = strings.TrimRight(note.Body, "\n") + fmt.Sprintf("\n%d", n)
				_, err = m.WriteNote(ctx, note)
				return err
			}))
		})
	}
	wg.Wait()

	final, err := m.Store().ReadNote(seeded.FilePath)
	require.NoError(t, err)
	lines := bodyLines(final.Body)
	require.Len(t, lines, writers+1, "each writer must see the previous writer's line")
	for i, line := range lines {
		require.Equal(t, fmt.Sprint(i), line, "numbering must be gapless; body was:\n%s", final.Body)
	}
}

// Different files must not serialize against each other: the lock is per path,
// not one global mutex, or every concurrent agent would queue behind one slow
// write to an unrelated memory.
func TestMutate_DifferentPathsDoNotBlockEachOther(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	a := seedMemory(t, m, "alpha", "a")
	b := seedMemory(t, m, "beta", "b")

	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- m.Mutate(ctx, a.FilePath, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	// beta must complete while alpha is held; a global lock would deadlock here
	// until the test's timeout.
	require.NoError(t, m.Mutate(ctx, b.FilePath, func(context.Context) error { return nil }))
	close(release)
	require.NoError(t, <-done)
}

// A waiter whose request is abandoned must fail with its own context rather than
// pinning a goroutine behind a slow mutation.
func TestMutate_WaiterHonorsContextCancellation(t *testing.T) {
	m, _ := newManager(t)
	seeded := seedMemory(t, m, "held", "x")

	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- m.Mutate(context.Background(), seeded.FilePath, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := m.Mutate(ctx, seeded.FilePath, func(context.Context) error {
		t.Fatal("fn must not run when the lock was never acquired")
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)

	close(release)
	require.NoError(t, <-done)
	// The holder released cleanly, so the path is takeable again.
	require.NoError(t, m.Mutate(context.Background(), seeded.FilePath, func(context.Context) error { return nil }))
}

// ErrNoChange is a control signal: no write, no error, and the loaded item comes
// back -- which is what keeps an already-starred favorite_set from rewriting the
// file and bumping it through the indexer for nothing.
func TestMutateMemory_ErrNoChangeSkipsTheWrite(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	seeded := seedMemory(t, m, "unchanged", "body")

	out, err := m.MutateMemory(ctx, seeded.FilePath, func(_ context.Context, mem core.Memory) (core.Memory, error) {
		mem.Body = "this must never be written"
		return mem, ErrNoChange
	})
	require.NoError(t, err)
	require.Equal(t, seeded.ID, out.ID)
	require.Equal(t, "body", strings.TrimSpace(out.Body))

	onDisk, err := m.Store().ReadMemory(seeded.FilePath)
	require.NoError(t, err)
	require.Equal(t, "body", strings.TrimSpace(onDisk.Body))
}

// A callback failure aborts the whole mutation: the file must be exactly as it
// was, not half-written.
func TestMutateMemory_CallbackErrorWritesNothing(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	seeded := seedMemory(t, m, "aborted", "original")

	boom := errors.New("boom")
	_, err := m.MutateMemory(ctx, seeded.FilePath, func(_ context.Context, mem core.Memory) (core.Memory, error) {
		mem.Body = "replaced"
		return mem, boom
	})
	require.ErrorIs(t, err, boom)

	onDisk, err := m.Store().ReadMemory(seeded.FilePath)
	require.NoError(t, err)
	require.Equal(t, "original", strings.TrimSpace(onDisk.Body))
}

// Two spellings of one path must take the SAME lock, or normalization becomes a
// silent hole in the serialization.
func TestMutate_PathSpellingsShareOneLock(t *testing.T) {
	m, _ := newManager(t)
	seeded := seedMemory(t, m, "spelled", "x")

	entered := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = m.Mutate(context.Background(), seeded.FilePath, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	alias := strings.Replace(seeded.FilePath, "/", "//", 1)
	err := m.Mutate(ctx, alias, func(context.Context) error { return nil })
	require.ErrorIs(t, err, context.DeadlineExceeded, "an alias spelling must block on the same lock")
	close(release)
}

// Passing one path twice must not self-deadlock: MutatePaths dedupes.
func TestMutatePaths_DuplicatesCollapse(t *testing.T) {
	m, _ := newManager(t)
	seeded := seedMemory(t, m, "twice", "x")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, m.MutatePaths(ctx, []string{seeded.FilePath, seeded.FilePath}, func(context.Context) error {
		return nil
	}))
}

// A path outside the data dir is refused before fn runs -- the same boundary
// every other file operation applies, just earlier.
func TestMutate_RefusesEscapingPath(t *testing.T) {
	m, _ := newManager(t)
	err := m.Mutate(context.Background(), "../escape.md", func(context.Context) error {
		t.Fatal("fn must not run for a path outside the data dir")
		return nil
	})
	require.Error(t, err)
}
