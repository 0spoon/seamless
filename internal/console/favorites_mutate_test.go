package console

// The owner side of the same star. A console toggle rewrites the whole markdown
// file, so it has the agent surface's lost-update problem with an extra edge:
// the owner starring a memory is most likely to happen while an agent is
// actively writing to it, which is precisely when the star was overwriting the
// agent's work.

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/files"
)

func TestConsoleFavorite_ConcurrentWritersLoseNothing(t *testing.T) {
	ctx := context.Background()
	_, mgr, mux := newConsoleWithFiles(t)
	m := writeMemory(t, mgr, core.KindGotcha, "seamless", "raced", "starred while written")
	relPath := files.MemoryRelPath("seamless", "raced")

	const appenders, starrers = 5, 3
	appendErrs := make([]error, appenders)
	starCodes := make([]int, starrers)

	// Goroutines only collect; testify's FailNow off the test goroutine would
	// abandon the WaitGroup rather than fail the test.
	var wg sync.WaitGroup
	wg.Add(appenders + starrers)
	for i := range appenders {
		go func() {
			defer wg.Done()
			_, appendErrs[i] = mgr.MutateMemory(ctx, relPath, func(_ context.Context, mem core.Memory) (core.Memory, error) {
				mem.Body += "\nmarker-" + strconv.Itoa(i)
				return mem, nil
			})
		}()
	}
	for i := range starrers {
		go func() {
			defer wg.Done()
			starCodes[i] = postFavorite(mux, "memory", m.ID, url.Values{"favorite": {"1"}}).Code
		}()
	}
	wg.Wait()

	for i, aerr := range appendErrs {
		require.NoError(t, aerr, "appender %d", i)
	}
	for i, code := range starCodes {
		require.Equal(t, http.StatusSeeOther, code, "starrer %d", i)
	}

	onDisk, err := mgr.Store().ReadMemory(relPath)
	require.NoError(t, err)
	require.True(t, onDisk.Favorite, "the star landed")
	for i := range appenders {
		require.Contains(t, onDisk.Body, "marker-"+strconv.Itoa(i),
			"the console star rewrote the whole file and dropped a concurrent append")
	}
}

// A second star on an already-starred note changes nothing, so it must write
// nothing: re-rendering an identical file would re-index it, re-embed it, and
// move the mtime the watcher and the owner's editor both read as a change.
func TestConsoleFavorite_AlreadyStarredWritesNothing(t *testing.T) {
	ctx := context.Background()
	_, mgr, mux := newConsoleWithFiles(t)

	id, err := core.NewID()
	require.NoError(t, err)
	created := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	note, err := mgr.WriteNote(ctx, core.Note{
		ID: id, Slug: "pinned-note", Title: "Pinned", Description: "already starred",
		Project: "seamless", Body: "the body", Created: created, Updated: created,
	})
	require.NoError(t, err)

	require.Equal(t, http.StatusSeeOther,
		postFavorite(mux, "note", note.ID, url.Values{"favorite": {"1"}}).Code)

	relPath := files.NoteRelPath("seamless", "pinned-note")
	before, err := mgr.Store().ReadNote(relPath)
	require.NoError(t, err)
	require.True(t, before.Favorite)

	// Backdating is the assertion: any write at all, identical bytes or not,
	// stamps a fresh mtime.
	abs := filepath.Join(mgr.Store().DataDir(), relPath)
	past := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(abs, past, past))

	require.Equal(t, http.StatusSeeOther,
		postFavorite(mux, "note", note.ID, url.Values{"favorite": {"1"}}).Code)

	info, err := os.Stat(abs)
	require.NoError(t, err)
	require.True(t, info.ModTime().Equal(past),
		"an already-starred note must not be rewritten: mtime moved to %s", info.ModTime())

	after, err := mgr.Store().ReadNote(relPath)
	require.NoError(t, err)
	require.Equal(t, before.ContentHash, after.ContentHash)
	require.True(t, created.Equal(after.Updated), "a star is not authorship: updated must not move")
}
