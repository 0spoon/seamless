package mcp

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// TestCatalogMatchesRegistration is the contract that keeps the generated docs
// honest: Catalog must list exactly the tools registerTools registers, in the
// same order. A new tool wired into only one of the two trips this rather than
// quietly going undocumented (or documenting a tool the server never serves).
// It is a same-package test so it can read the server's private toolNames --
// the registration record itself, not a second hand-maintained list.
func TestCatalogMatchesRegistration(t *testing.T) {
	srv := New(Config{})

	names := make([]string, 0, len(Catalog()))
	for _, tool := range Catalog() {
		names = append(names, tool.Name)
	}

	require.Equal(t, srv.toolNames, names, "Catalog order/content must mirror registerTools")
	require.Len(t, names, ToolCount)
}

// TestAddToolRecordsEverySchema pins what makes validation impossible to forget:
// addTool records the schema in the same statement that registers the tool, so a
// registered tool cannot lack one. Without this, a tool added through some other
// path would dispatch unvalidated -- silently restoring the pre-validator
// behavior for exactly that tool, which is the failure mode hardest to notice.
//
// Same-package so it reads the registration record itself rather than a second
// hand-maintained list.
func TestAddToolRecordsEverySchema(t *testing.T) {
	srv := New(Config{})

	require.Len(t, srv.toolSchemas, ToolCount)
	for _, name := range srv.toolNames {
		schema, ok := srv.toolSchema(name)
		require.True(t, ok, "%s: registered with no input schema -- it would dispatch unvalidated", name)
		require.Equal(t, "object", schema.Type, "%s: an input schema is a JSON object", name)
	}
}

// A tool the server never registered fails closed rather than dispatching
// unvalidated. Unreachable through addTool by construction (the test above), so
// this pins the fallback itself: mcp-go's per-session tools are the only path
// that could produce one, and an unvalidatable call must not proceed.
func TestValidateMiddlewareFailsClosedWithoutASchema(t *testing.T) {
	srv := New(Config{})

	var reached bool
	h := srv.validateMiddleware(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		reached = true
		return mcp.NewToolResultText("ok"), nil
	})
	res, err := h(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: "never_registered"},
	})

	require.NoError(t, err)
	require.True(t, res.IsError)
	require.False(t, reached, "an unvalidatable call must not reach its handler")
}

// TestCatalogToolsAreDocumentable guards the inputs docsgen renders: a tool with
// no description would emit an empty docs section, and a duplicate name would
// collide on the page anchor (/docs/reference/mcp/tasks/#tasks_claim).
func TestCatalogToolsAreDocumentable(t *testing.T) {
	seen := make(map[string]bool)
	for _, tool := range Catalog() {
		require.NotEmpty(t, tool.Name, "every catalog tool has a name")
		require.NotEmpty(t, tool.Description, "%s: description is the docs body", tool.Name)
		require.False(t, seen[tool.Name], "duplicate tool name %q", tool.Name)
		seen[tool.Name] = true
	}
}
