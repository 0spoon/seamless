package files

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newBareWatcher builds a watcher over a temp dir without a Manager, so tests
// can drive debounceFire/close directly.
func newBareWatcher(t *testing.T, handler changeHandler) *watcher {
	t.Helper()
	w, err := newWatcher(t.TempDir(), handler, 0, nil)
	require.NoError(t, err)
	return w
}

// Regression: close() must drain an in-flight debounce handler before returning.
// Before the fix it returned immediately, so the daemon's shutdown could close
// the DB while a re-index handler was still writing to it.
func TestWatcherClose_WaitsForInFlightHandler(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	w := newBareWatcher(t, func(ctx context.Context, relPath string) error {
		close(entered)
		<-release
		return nil
	})

	// debounce 0: the timer fires immediately and the handler blocks in fire().
	w.debounceFire(context.Background(), "/tmp/x.md", "memory/_global/x.md")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("debounce handler never started")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- w.close() }()

	// close() must not return while the handler is still running.
	select {
	case <-closeDone:
		t.Fatal("close returned while a handler invocation was still in flight")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("close did not return after the handler finished")
	}
}

// After close() returns, no debounce handler may start: a pending timer is
// stopped, a re-arm attempt is refused, and a timer that was already due fails
// the closed check. The handler count must stay frozen.
func TestWatcherClose_NoHandlerStartsAfterClose(t *testing.T) {
	var fires atomic.Int64
	w := newBareWatcher(t, func(ctx context.Context, relPath string) error {
		fires.Add(1)
		return nil
	})
	w.debounce = time.Hour // armed but never due before close stops it

	ctx := context.Background()
	w.debounceFire(ctx, "/tmp/a.md", "memory/_global/a.md")
	require.NoError(t, w.close())
	before := fires.Load()

	// Arming after close must be a no-op, not a timer close() can never see.
	w.debounceFire(ctx, "/tmp/b.md", "memory/_global/b.md")
	w.mu.Lock()
	pending := len(w.pending)
	w.mu.Unlock()
	require.Zero(t, pending, "no timer may be armed after close")
	require.Equal(t, before, fires.Load())

	// Idempotent: a second close neither panics nor double-closes.
	require.NoError(t, w.close())
}
