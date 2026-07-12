package mcp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/store"
)

// callErr calls a tool and returns (isError, text) without failing the test, for
// asserting on rejections.
func callErr(t *testing.T, ctx context.Context, cli interface {
	CallTool(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
}, name string, args map[string]any) (bool, string) {
	t.Helper()
	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
	require.NoError(t, err)
	return res.IsError, resultText(t, res)
}

// TestBodyContentTextAliases verifies the item-text param is accepted under any
// of body/content/text, so an agent primed on one tool's name succeeds on the
// append tools (the top field-name mistake in the logs).
func TestBodyContentTextAliases(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	// memory_write accepts "content" in place of "body".
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "aliased", "kind": "reference", "description": "d",
		"content": "written via content alias", "project": "global",
	})
	r := callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "aliased", "project": "global"})
	require.Contains(t, r["body"], "written via content alias")

	// memory_append accepts "body" in place of "content".
	callJSON(t, ctx, cli, "memory_append", map[string]any{
		"name": "aliased", "body": "appended via body alias", "project": "global",
	})
	r = callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "aliased", "project": "global"})
	require.Contains(t, r["body"], "appended via body alias")

	// notes_append accepts "content" in place of its historical "text".
	nc := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "a note", "body": "seed", "project": "global",
	})
	noteID, _ := nc["id"].(string)
	require.NotEmpty(t, noteID)
	isErr, txt := callErr(t, ctx, cli, "notes_append", map[string]any{"id": noteID, "content": "line via content"})
	require.False(t, isErr, txt)
}

// TestGlobalNamespaceExplicit verifies project=global targets the global scope
// deliberately, and is readable back as a global memory.
func TestGlobalNamespaceExplicit(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	w := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "a-global-fact", "kind": "reference", "description": "d",
		"body": "b", "project": "global",
	})
	require.Equal(t, "", w["project"], "project=global normalizes to the empty global scope")

	_, found, err := store.MemoryByName(ctx, db, "", "a-global-fact")
	require.NoError(t, err)
	require.True(t, found, "the memory lands in the global scope")
}

// TestAmbiguousScopeRejected verifies a durable create with no bound session, no
// ambient session, and no explicit project is rejected rather than silently
// landing in global.
func TestAmbiguousScopeRejected(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	isErr, txt := callErr(t, ctx, cli, "memory_write", map[string]any{
		"name": "orphan", "kind": "reference", "description": "d", "body": "b",
	})
	require.True(t, isErr, "unscoped create must be rejected")
	require.Contains(t, txt, "ambiguous scope")

	// The same create with an explicit scope succeeds.
	isErr, txt = callErr(t, ctx, cli, "memory_write", map[string]any{
		"name": "orphan", "kind": "reference", "description": "d", "body": "b", "project": "global",
	})
	require.False(t, isErr, txt)
}

// TestSessionEndAcceptsLongFindings verifies the old 1500-char cap is gone:
// long findings are stored in full, not rejected.
func TestSessionEndAcceptsLongFindings(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	start := callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})
	sessID, _ := start["session_id"].(string)
	require.NotEmpty(t, sessID)

	long := strings.Repeat("x", 5000)
	end := callJSON(t, ctx, cli, "session_end", map[string]any{"findings": long})
	require.Equal(t, "completed", end["status"])

	sess, ok, err := store.SessionByID(ctx, db, sessID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 5000, len(sess.Findings), "long findings are stored in full")
}

// TestProjectCreateRejectsReservedSlug verifies the global namespace token
// cannot be claimed as a real project slug.
func TestProjectCreateRejectsReservedSlug(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	for _, slug := range []string{"global", "_global"} {
		isErr, txt := callErr(t, ctx, cli, "project_create", map[string]any{"name": "X", "slug": slug})
		require.True(t, isErr, "slug %q must be rejected", slug)
		require.Contains(t, txt, "reserved")
	}
}
