package mcp

import (
	"testing"

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
