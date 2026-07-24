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
	ret, ok := out["retrieval"].(map[string]any)
	require.True(t, ok)
	_, ok = out["eventsByKind"].(map[string]any)
	require.True(t, ok)

	// The read-after-inject funnel split rides on the retrieval section: one
	// entry per injection surface, in a stable order.
	funnel, ok := ret["funnelBySurface"].([]any)
	require.True(t, ok, "retrieval carries funnelBySurface")
	require.Len(t, funnel, 2)
	first, ok := funnel[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "session-start", first["surface"])
	second, ok := funnel[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "subagent-start", second["surface"])
}

// TestToolCountMatchesRegistered mirrors the doctor assertion: the server must
// register exactly ToolCount tools (26 at P4; 28 with tasks_claim + tasks_release;
// 29 with gardener_request; 30 with gardener_split; 31 with favorite_set).
func TestToolCountMatchesRegistered(t *testing.T) {
	srv := mcpserver.New(mcpserver.Config{})
	require.Equal(t, mcpserver.ToolCount, srv.NumTools())
	require.Equal(t, 31, mcpserver.ToolCount)
}
