package mcp_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/store"
)

// Model attribution flows session -> knowledge: session_start records the
// self-reported model id verbatim, memory_write/notes_create stamp it, a
// rewrite by a model-less session preserves it, and a rewrite by a different
// model re-attributes the content.
func TestModelAttribution(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)

	// Agent 1 reports its model.
	cli1 := dialClient(t, ctx, url, testKey)
	start := callJSON(t, ctx, cli1, "session_start", map[string]any{
		"cwd": "/work/demo", "source": "startup", "model": "claude-fable-5",
	})
	sess, ok, err := store.SessionByID(ctx, db, start["session_id"].(string))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "claude-fable-5", sess.Model)

	callJSON(t, ctx, cli1, "memory_write", map[string]any{
		"name": "attribution-check", "kind": "gotcha",
		"description": "who wrote this memory",
		"body":        "Written by the fable session.\n",
	})
	r := callJSON(t, ctx, cli1, "memory_read", map[string]any{"name": "attribution-check"})
	require.Equal(t, "claude-fable-5", r["model"])

	nc := callJSON(t, ctx, cli1, "notes_create", map[string]any{
		"title": "Attribution writeup", "body": "Also written by the fable session.",
	})
	nr := callJSON(t, ctx, cli1, "notes_read", map[string]any{"id": nc["id"].(string)})
	require.Equal(t, "claude-fable-5", nr["model"])
	callJSON(t, ctx, cli1, "session_end", map[string]any{"findings": "done"})

	// Agent 2 never reports a model: its rewrite keeps the known producer
	// rather than erasing it with "".
	cli2 := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli2, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})
	callJSON(t, ctx, cli2, "memory_write", map[string]any{
		"name": "attribution-check", "kind": "gotcha",
		"description": "who wrote this memory",
		"body":        "Rewritten by a session with no known model.\n",
	})
	r = callJSON(t, ctx, cli2, "memory_read", map[string]any{"name": "attribution-check"})
	require.Equal(t, "claude-fable-5", r["model"], "an unknown model must not erase attribution")
	callJSON(t, ctx, cli2, "session_end", map[string]any{"findings": "done"})

	// Agent 3 runs a different model: its rewrite re-attributes the content.
	cli3 := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli3, "session_start", map[string]any{
		"cwd": "/work/demo", "source": "startup", "model": "gpt-5.5",
	})
	callJSON(t, ctx, cli3, "memory_write", map[string]any{
		"name": "attribution-check", "kind": "gotcha",
		"description": "who wrote this memory",
		"body":        "Rewritten by the gpt session.\n",
	})
	r = callJSON(t, ctx, cli3, "memory_read", map[string]any{"name": "attribution-check"})
	require.Equal(t, "gpt-5.5", r["model"], "a rewrite by a known model re-attributes the content")
}
