package mcp_test

// favorite_set is a whole-file rewrite wearing a one-bit change: it renders the
// memory's frontmatter AND body from what it read. These tests cover the two
// halves of that -- the write it must not lose, and the write it must not make.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/files"
)

// Starring a memory another writer is appending to used to erase the append: the
// star read the pre-append body, and its rename put that body back with no error
// anywhere (the index upsert is keyed by id, so nothing complained). The star and
// the appends here go through the same files manager the server writes with, which
// is the lock every durable write in the daemon takes.
func TestFavoriteSet_ConcurrentWritersLoseNothing(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)

	id, err := core.NewID()
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = mgr.WriteMemory(ctx, core.Memory{
		ID: id, Kind: core.KindGotcha, Name: "raced", Description: "starred while written",
		Project: "demo", Body: "original body", Created: now, Updated: now, ValidFrom: now,
	})
	require.NoError(t, err)
	relPath := files.MemoryRelPath("demo", "raced")

	// One client per starring goroutine: a shared connection would serialize the
	// calls it is the point of this test to overlap.
	const starrers = 3
	clients := make([]*mcpclient.Client, starrers)
	for i := range clients {
		clients[i] = dialClient(t, ctx, url, testKey)
	}

	const appenders = 5
	appendErrs := make([]error, appenders)
	starResults := make([]*mcp.CallToolResult, starrers)
	starErrs := make([]error, starrers)

	// Nothing in a goroutine asserts: testify's FailNow off the test goroutine
	// would abandon the WaitGroup instead of failing the test. Results are
	// collected and judged after the join.
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
			// project is explicit so every goroutine resolves the same scope without
			// racing over one connection's session binding.
			starResults[i], starErrs[i] = clients[i].CallTool(ctx, mcp.CallToolRequest{
				Params: mcp.CallToolParams{Name: "favorite_set", Arguments: map[string]any{
					"kind": "memory", "id": "raced", "favorite": true, "project": "demo",
				}},
			})
		}()
	}
	wg.Wait()

	for i, aerr := range appendErrs {
		require.NoError(t, aerr, "appender %d", i)
	}
	for i, serr := range starErrs {
		require.NoError(t, serr, "starrer %d", i)
		require.False(t, starResults[i].IsError, "starrer %d: %s", i, resultText(t, starResults[i]))
	}

	onDisk, err := mgr.Store().ReadMemory(relPath)
	require.NoError(t, err)
	require.True(t, onDisk.Favorite, "the star landed")
	for i := range appenders {
		require.Contains(t, onDisk.Body, "marker-"+strconv.Itoa(i),
			"the star rewrote the whole file and dropped a concurrent append")
	}
}

// Re-starring an already-starred memory must write nothing at all. The flag is
// already right, so a write would only re-render, re-index and re-embed an
// identical file -- and re-stamp the mtime the owner's editor and the watcher
// both read as "this changed".
func TestFavoriteSet_AlreadyStarredWritesNothing(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "fav-idempotent"})

	id, err := core.NewID()
	require.NoError(t, err)
	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	_, err = mgr.WriteMemory(ctx, core.Memory{
		ID: id, Kind: core.KindGotcha, Name: "pinned", Description: "already starred",
		Project: "demo", Body: "the body", Created: created, Updated: created, ValidFrom: created,
	})
	require.NoError(t, err)

	relPath := files.MemoryRelPath("demo", "pinned")
	star := map[string]any{"kind": "memory", "id": "pinned", "favorite": true}
	require.Equal(t, true, callJSON(t, ctx, cli, "favorite_set", star)["favorite"])

	before, err := mgr.Store().ReadMemory(relPath)
	require.NoError(t, err)
	require.True(t, before.Favorite)

	// Backdate the file: any rewrite at all, identical bytes or not, stamps a
	// fresh mtime, so this is the assertion that no write happened -- not merely
	// that the content came out the same.
	abs := filepath.Join(mgr.Store().DataDir(), relPath)
	past := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(abs, past, past))

	require.Equal(t, true, callJSON(t, ctx, cli, "favorite_set", star)["favorite"],
		"a redundant star still reports the requested state")

	info, err := os.Stat(abs)
	require.NoError(t, err)
	require.True(t, info.ModTime().Equal(past),
		"an already-starred memory must not be rewritten: mtime moved to %s", info.ModTime())

	after, err := mgr.Store().ReadMemory(relPath)
	require.NoError(t, err)
	require.Equal(t, before.ContentHash, after.ContentHash)
	require.True(t, created.Equal(after.Updated), "a star is not authorship: updated must not move")
}
