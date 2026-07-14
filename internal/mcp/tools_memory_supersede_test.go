package mcp_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMemoryWrite_SupersedeFailureIsToolError covers the write-succeeds,
// supersede-fails split: the call must surface an explicit tool error (not a
// success payload with an embedded error field an agent would read past), the
// new memory is kept rather than rolled back, and the still-active state of
// the target is spelled out. A retry with the corrected target then completes
// the supersession losslessly via the in-place update.
func TestMemoryWrite_SupersedeFailureIsToolError(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "old-truth", "kind": "gotcha",
		"description": "the old understanding",
		"body":        "old body\n",
	})

	// The supersede target does not exist: the write lands, the call errors.
	isErr, txt := callErr(t, ctx, cli, "memory_write", map[string]any{
		"name": "new-truth", "kind": "gotcha",
		"description": "the new understanding",
		"body":        "new body\n",
		"supersedes":  "no-such-memory",
	})
	require.True(t, isErr, "a failed supersede must be a tool error, not a field in a success payload")
	require.Contains(t, txt, "written and kept")
	require.Contains(t, txt, "no-such-memory")
	require.Contains(t, txt, "STILL ACTIVE")

	// The new memory was kept, not rolled back, and reads back active; the
	// error names its id so the agent keeps the reference.
	r := callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "new-truth"})
	require.Contains(t, r["body"], "new body")
	require.Nil(t, r["warning"])
	require.Contains(t, txt, r["id"].(string), "the error must carry the written memory's id")

	// Retrying with the corrected target is an in-place update (same id) that
	// completes the supersession.
	w := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "new-truth", "kind": "gotcha",
		"description": "the new understanding",
		"body":        "new body\n",
		"supersedes":  "old-truth",
	})
	require.Equal(t, true, w["updated"])
	require.Equal(t, r["id"], w["id"])
	require.Equal(t, "demo/old-truth", w["superseded"])

	old := callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "old-truth"})
	require.Contains(t, old["warning"], "superseded by demo/new-truth")
}

// TestMemoryWrite_SupersedeRetryIdempotent pins the retry semantics the
// partial-failure error prescribes: re-issuing a memory_write whose supersede
// already landed succeeds again (the target being superseded by this exact
// replacement is the goal state), while a DIFFERENT memory claiming an
// already-superseded target is a tool error -- with that new memory still kept.
func TestMemoryWrite_SupersedeRetryIdempotent(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "old-truth", "kind": "gotcha",
		"description": "the old understanding",
		"body":        "old body\n",
	})
	args := map[string]any{
		"name": "new-truth", "kind": "gotcha",
		"description": "the new understanding",
		"body":        "new body\n",
		"supersedes":  "old-truth",
	}
	w := callJSON(t, ctx, cli, "memory_write", args)
	require.Equal(t, "demo/old-truth", w["superseded"])

	// Exact retry: already superseded by this same replacement -> success.
	w2 := callJSON(t, ctx, cli, "memory_write", args)
	require.Equal(t, true, w2["updated"])
	require.Equal(t, w["id"], w2["id"])
	require.Equal(t, "demo/old-truth", w2["superseded"])

	// A different memory claiming the same target fails explicitly.
	isErr, txt := callErr(t, ctx, cli, "memory_write", map[string]any{
		"name": "third-truth", "kind": "gotcha",
		"description": "a rival understanding",
		"body":        "third body\n",
		"supersedes":  "old-truth",
	})
	require.True(t, isErr)
	require.Contains(t, txt, "already superseded")

	// The rival write itself is still kept.
	r := callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "third-truth"})
	require.Equal(t, "third-truth", r["name"])
	require.Nil(t, r["warning"])
}
