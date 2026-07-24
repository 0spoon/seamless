package mcp_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/agentguide"
)

// A kind=stage write whose body carries no parseable Status header succeeds but
// returns the stage_hint advisory, teaching the header contract in-session --
// the same non-blocking pattern as the similar-memory hint.
func TestMemoryWriteStageHint(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	// Headerless body: the write proceeds, the hint fires.
	w := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "pr-review-wait", "kind": "stage", "description": "waiting on maintainer",
		"body": "PR 123 awaits maintainer re-review, nothing actionable here",
	})
	require.NotEmpty(t, w["id"], "the write itself must proceed")
	hint, _ := w["stage_hint"].(string)
	require.Contains(t, hint, "no parseable Status header")
	require.Contains(t, hint, agentguide.StageContract, "the hint teaches the shared contract, not a paraphrase")

	// A valid header (any live gate or done) silences the hint.
	w = callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "pr-review-wait", "kind": "stage", "description": "waiting on maintainer",
		"body": "Status: blocked\nGate: human\n\nPR 123 awaits maintainer re-review",
	})
	require.NotContains(t, w, "stage_hint")

	// An unrecognized status token is a broken header, named in the hint.
	w = callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "odd-status", "kind": "stage", "description": "odd status token",
		"body": "Status: wip\n\nnarrative",
	})
	require.Contains(t, w["stage_hint"], `unrecognized Status value "wip"`)

	// Non-stage kinds never hint, whatever the body says.
	w = callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "plain-gotcha", "kind": "gotcha", "description": "no headers here",
		"body": "a trap and how to avoid it",
	})
	require.NotContains(t, w, "stage_hint")
}

// memory_append to a stage hints from the POST-append body: appending prose to
// a headerless stage leaves it headerless (append cannot change the header),
// while an append that lands a valid header inside the parse window is a fix.
func TestMemoryAppendStageHint(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "headerless-stage", "kind": "stage", "description": "no header yet",
		"body": "waiting on the vendor",
	})
	a := callJSON(t, ctx, cli, "memory_append", map[string]any{
		"name": "headerless-stage", "body": "still waiting, pinged them again",
	})
	require.Equal(t, "appended", a["status"])
	require.Contains(t, a["stage_hint"], "no parseable Status header")

	// The appended lines land within the header parse window of this short body
	// and carry a valid Status, so the stage is fixed and no hint fires.
	a = callJSON(t, ctx, cli, "memory_append", map[string]any{
		"name": "headerless-stage", "body": "Status: blocked\nGate: human",
	})
	require.NotContains(t, a, "stage_hint")

	// Appending prose to a stage with a live header is the intended use: silent.
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "gated-stage", "kind": "stage", "description": "properly gated",
		"body": "Status: in_progress\nGate: ai\n\nnarrative",
	})
	a = callJSON(t, ctx, cli, "memory_append", map[string]any{
		"name": "gated-stage", "body": "progress note",
	})
	require.NotContains(t, a, "stage_hint")
}
