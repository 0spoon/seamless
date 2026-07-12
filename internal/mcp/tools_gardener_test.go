package mcp_test

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/store"
)

func TestGardenerProposalsAndApply(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	// Create a memory through the tool so it exists on disk + index; an archive
	// apply then retires it.
	wrote := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "stale-thing", "kind": "gotcha",
		"description": "an old memory", "body": "body text", "project": "global",
	})
	memID, _ := wrote["id"].(string)
	require.NotEmpty(t, memID)

	// Seed archive + digest proposals directly.
	arch, err := store.CreateProposal(ctx, db, store.ProposalArchive, map[string]any{
		"key": "archive:" + memID, "id": memID, "name": "stale-thing",
	})
	require.NoError(t, err)
	dig, err := store.CreateProposal(ctx, db, store.ProposalDigest, map[string]any{
		"key": "digest:demo:2026-07", "project": "",
		"title": "Session digest -- 2026-07", "body": "- did work",
	})
	require.NoError(t, err)

	// gardener_proposals lists both; the kind filter narrows.
	all := callJSON(t, ctx, cli, "gardener_proposals", nil)
	require.Equal(t, float64(2), all["count"])
	archives := callJSON(t, ctx, cli, "gardener_proposals", map[string]any{"kind": "archive"})
	require.Equal(t, float64(1), archives["count"])

	// Apply the archive: the memory is retired (leaves the active index).
	applied := callJSON(t, ctx, cli, "gardener_apply", map[string]any{"id": arch.ID})
	require.Equal(t, "applied", applied["status"])
	require.Equal(t, "archive", applied["kind"])
	_, found, err := store.MemoryByName(ctx, db, "", "stale-thing")
	require.NoError(t, err)
	require.False(t, found, "archived memory must leave the active index")

	// Dismiss the digest: no side effect, just resolved.
	dismissed := callJSON(t, ctx, cli, "gardener_apply", map[string]any{"id": dig.ID, "action": "dismiss"})
	require.Equal(t, "dismissed", dismissed["status"])

	// Nothing pending now.
	none := callJSON(t, ctx, cli, "gardener_proposals", nil)
	require.Equal(t, float64(0), none["count"])

	// Applying an already-resolved proposal is a tool error, not a transport error.
	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "gardener_apply", Arguments: map[string]any{"id": arch.ID},
	}})
	require.NoError(t, err)
	require.True(t, res.IsError)
}
