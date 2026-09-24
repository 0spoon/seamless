package mcp_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/files"
)

// callToolAsync is CallTool without any testing.T interaction, so it is safe to
// run from a goroutine: require's FailNow may only be called from the test's own
// goroutine, and the concurrency tests below fan out by definition. It reports
// the tool error as a plain error instead.
func callToolAsync(ctx context.Context, cli *mcpclient.Client, name string, args map[string]any) error {
	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
	if err != nil {
		return err
	}
	if !res.IsError {
		return nil
	}
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return errors.New(tc.Text)
		}
	}
	return fmt.Errorf("%s: tool error with no text content", name)
}

// The lost update, at the tool surface. Every mutating memory handler used to
// read the file, edit the struct, and rename a whole new file over it; two
// agents appending to the same memory both read the same starting body and the
// second rename kept only its own line, with no error anywhere -- the index
// upsert is keyed by id, so even the UNIQUE file_path constraint stayed quiet.
//
// Each appender gets its own MCP client, which is what makes these separate
// bound sessions rather than one agent talking to itself: this is the shape the
// bug actually took (parallel subagents sharing a findings memory).
func TestMemoryAppend_ConcurrentAppendsLoseNothing(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)

	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "append-race-seed"})
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "shared-findings", "kind": "gotcha",
		"description": "what the parallel agents found", "body": "base line",
	})

	const appenders = 8
	clients := make([]*mcpclient.Client, appenders)
	for i := range clients {
		clients[i] = dialClient(t, ctx, url, testKey)
		callJSON(t, ctx, clients[i], "session_start", map[string]any{
			"cwd": "/work/demo", "name": fmt.Sprintf("append-race-%02d", i),
		})
	}

	// One release for everyone, so the calls actually overlap instead of queueing
	// behind each other's setup.
	start := make(chan struct{})
	errs := make([]error, appenders)
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = callToolAsync(ctx, clients[i], "memory_append", map[string]any{
				"name": "shared-findings", "body": fmt.Sprintf("finding-%02d", i),
			})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "appender %d", i)
	}

	// The file is the source of truth, so assert there rather than on the index.
	onDisk, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "shared-findings"))
	require.NoError(t, err)
	require.Contains(t, onDisk.Body, "base line", "the starting body must survive every append")
	for i := range appenders {
		require.Contains(t, onDisk.Body, fmt.Sprintf("finding-%02d", i),
			"append %d was overwritten by a concurrent one -- the lost update is back", i)
	}
}

// The same race on memory_write's update-in-place path: concurrent rewrites of
// one name must each land whole, and none may resurrect a stale body. The last
// writer's body is whichever won the lock -- unknowable and fine -- but it must
// be exactly one caller's body, and identity must not fork.
func TestMemoryWrite_ConcurrentWritesSerialize(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "write-race-seed"})

	const writers = 8
	clients := make([]*mcpclient.Client, writers)
	for i := range clients {
		clients[i] = dialClient(t, ctx, url, testKey)
		callJSON(t, ctx, clients[i], "session_start", map[string]any{
			"cwd": "/work/demo", "name": fmt.Sprintf("write-race-%02d", i),
		})
	}

	start := make(chan struct{})
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = callToolAsync(ctx, clients[i], "memory_write", map[string]any{
				"name": "contended", "kind": "decision",
				"description": fmt.Sprintf("desc-%02d", i),
				"body":        fmt.Sprintf("body-%02d", i),
			})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "writer %d", i)
	}

	onDisk, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "contended"))
	require.NoError(t, err)
	body := strings.TrimSpace(onDisk.Body)
	require.Regexp(t, `^body-\d{2}$`, body, "the file must hold exactly one writer's body, whole")
	// Description and body come from the same call, so a torn write would pair
	// one writer's body with another's description.
	require.Equal(t, "desc-"+strings.TrimPrefix(body, "body-"), onDisk.Description,
		"body and description must come from the same write")

	// Exactly one memory exists under that name: a create racing a create would
	// otherwise mint two ULIDs for one file.
	read := callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "contended"})
	require.Equal(t, onDisk.ID, read["id"])
}

// Extra (the unknown frontmatter keys the owner's own tooling puts there) is the
// one field with no second copy: core.Memory.Extra is deliberately not mirrored
// to the index. memory_write used to log a warning and fall back to the index
// values when the pre-write re-read failed, which rendered the file WITHOUT
// those keys and destroyed them -- while reporting success. Now the write is
// refused instead.
//
// The failure is staged by replacing the markdown file with a directory: the
// files layer refuses anything that is not a regular file, so the read fails
// deterministically without depending on how the test process's uid interacts
// with a chmod.
func TestMemoryWrite_RefusesWhenPreservationReadFails(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, dir := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "preserve-fail-sess"})

	// Seed through the files layer: Extra has no argument on the tool surface,
	// which is exactly why losing it was invisible.
	id, err := core.NewID()
	require.NoError(t, err)
	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	seeded := core.Memory{
		ID: id, Kind: core.KindGotcha, Name: "brittle", Description: "seeded description",
		Project: "demo", Body: "the original body", Tags: []string{"topic:dfu"},
		Favorite: true, Extra: map[string]any{"obsidian_plugin": "dataview", "owner_pinned": true},
		Created: created, Updated: created, ValidFrom: created,
	}
	_, err = mgr.WriteMemory(ctx, seeded)
	require.NoError(t, err)

	relPath := files.MemoryRelPath("demo", "brittle")
	absPath := filepath.Join(dir, filepath.FromSlash(relPath))
	original, err := os.ReadFile(absPath) //nolint:gosec // test-owned path under t.TempDir()
	require.NoError(t, err)

	require.NoError(t, os.Remove(absPath))
	require.NoError(t, os.Mkdir(absPath, 0o700))

	isErr, text := callErr(t, ctx, cli, "memory_write", map[string]any{
		"name": "brittle", "kind": "gotcha", "description": "corrected description",
		"body": "the corrected body",
	})
	require.True(t, isErr, "an unreadable existing memory must refuse the write, not degrade: %s", text)
	require.Contains(t, text, "refusing the write")
	require.Contains(t, text, relPath, "the error must name the file the agent has to fix")

	// Nothing was written: the staged directory is untouched, so no rename landed
	// on top of it.
	info, err := os.Lstat(absPath)
	require.NoError(t, err)
	require.True(t, info.IsDir(), "the refused write must not have replaced the path")

	// Restore the file and confirm the curation the degraded path would have
	// dropped is still there, byte for byte.
	require.NoError(t, os.Remove(absPath))
	require.NoError(t, os.WriteFile(absPath, original, 0o600))
	restored, err := mgr.Store().ReadMemory(relPath)
	require.NoError(t, err)
	require.Equal(t, "dataview", restored.Extra["obsidian_plugin"], "Extra must survive a refused write")
	require.Equal(t, true, restored.Extra["owner_pinned"])
	require.True(t, restored.Favorite)
	require.Equal(t, []string{"topic:dfu"}, restored.Tags)
	require.Equal(t, "the original body", strings.TrimSpace(restored.Body),
		"the refused write must not have replaced the body either")
}

// Model attribution follows the CONTENT everywhere, so an append re-stamps it:
// the prose was produced by the model appending it, and leaving the create-time
// value credits a model that never wrote those lines. SourceSession is the
// counterpart that stays create-only.
func TestMemoryAppend_RestampsModel(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	relPath := files.MemoryRelPath("demo", "attributed")

	writer := dialClient(t, ctx, url, testKey)
	created := callJSON(t, ctx, writer, "session_start", map[string]any{
		"cwd": "/work/demo", "name": "attrib-writer", "model": "model-alpha",
	})
	writerSession, _ := created["session_id"].(string)
	require.NotEmpty(t, writerSession)

	callJSON(t, ctx, writer, "memory_write", map[string]any{
		"name": "attributed", "kind": "reference", "description": "d", "body": "written by alpha",
	})
	onDisk, err := mgr.Store().ReadMemory(relPath)
	require.NoError(t, err)
	require.Equal(t, "model-alpha", onDisk.Model)
	require.Equal(t, writerSession, onDisk.SourceSession)

	// A different agent on a different model appends.
	appender := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, appender, "session_start", map[string]any{
		"cwd": "/work/demo", "name": "attrib-appender", "model": "model-beta",
	})
	callJSON(t, ctx, appender, "memory_append", map[string]any{
		"name": "attributed", "body": "appended by beta",
	})

	onDisk, err = mgr.Store().ReadMemory(relPath)
	require.NoError(t, err)
	require.Equal(t, "model-beta", onDisk.Model, "an append is a body change, so it re-stamps the model")
	require.Equal(t, writerSession, onDisk.SourceSession,
		"source_session records who CREATED the memory and a create happens once")
	require.Contains(t, onDisk.Body, "written by alpha")
	require.Contains(t, onDisk.Body, "appended by beta")

	// An unknown current model must never erase a known producer with "".
	anon := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, anon, "session_start", map[string]any{"cwd": "/work/demo", "name": "attrib-anon"})
	callJSON(t, ctx, anon, "memory_append", map[string]any{
		"name": "attributed", "body": "appended by an unidentified model",
	})
	onDisk, err = mgr.Store().ReadMemory(relPath)
	require.NoError(t, err)
	require.Equal(t, "model-beta", onDisk.Model, "an unknown model keeps the prior attribution")
	require.Contains(t, onDisk.Body, "appended by an unidentified model")
}

// Superseding rewrites the WHOLE old file from a read taken moments earlier, so
// it has to hold the same lock the appenders take -- otherwise a concurrent
// append is silently undone by the tombstone write, and losing content while
// marking a memory invalid is the worst place to lose it.
func TestMemoryWrite_SupersedeKeepsBodyAndTombstone(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "supersede-sess"})

	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "old-truth", "kind": "decision", "description": "the old call", "body": "the old reasoning",
	})
	callJSON(t, ctx, cli, "memory_append", map[string]any{"name": "old-truth", "body": "a later caveat"})

	out := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "new-truth", "kind": "decision", "description": "the new call",
		"body": "the new reasoning", "supersedes": "old-truth",
	})
	require.Equal(t, "demo/old-truth", out["superseded"])

	old, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "old-truth"))
	require.NoError(t, err)
	require.Contains(t, old.Body, "the old reasoning")
	require.Contains(t, old.Body, "a later caveat", "the tombstone appends to the real body, it does not replace it")
	require.Contains(t, old.Body, "Superseded by demo/new-truth")
	require.NotNil(t, old.InvalidAt)
	require.Equal(t, out["id"], old.SupersededBy)
}
