package mcp_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	mcpserver "github.com/0spoon/seamless/internal/mcp"
)

func TestUsageSummaryTool(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	// Write a memory and recall it, so the summary has something to report.
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "a-fact", "kind": "reference", "description": "a durable fact", "body": "body", "project": "global",
	})
	callJSON(t, ctx, cli, "recall", map[string]any{"query": "durable fact"})

	out := callJSON(t, ctx, cli, "usage_summary", nil)
	mems, ok := out["memories"].(map[string]any)
	require.True(t, ok, "summary has a memories section")
	require.Equal(t, float64(1), mems["active"])
	_, ok = out["retrieval"].(map[string]any)
	require.True(t, ok)
	_, ok = out["eventsByKind"].(map[string]any)
	require.True(t, ok)
}

// TestToolCountMatchesRegistered mirrors the doctor assertion: the server must
// register exactly ToolCount tools (26 at P4; 28 with tasks_claim + tasks_release).
func TestToolCountMatchesRegistered(t *testing.T) {
	srv := mcpserver.New(mcpserver.Config{})
	require.Equal(t, mcpserver.ToolCount, srv.NumTools())
	require.Equal(t, 28, mcpserver.ToolCount)
}
