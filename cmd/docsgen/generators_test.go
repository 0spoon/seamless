package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	seamlessmcp "github.com/0spoon/seamless/internal/mcp"
)

// repoRoot points the test at the repository root, the cwd docsgen requires:
// generators read repo files (seamless.yaml.example) by relative path, and `go
// test` runs from the package directory.
func repoRoot(t *testing.T) {
	t.Helper()
	t.Chdir("../..")
	_, err := os.Stat("go.mod")
	require.NoError(t, err, "test must run from the repo root")
}

// TestGenerateMCPToolsCoversCatalog renders every tool in the catalog through
// the generator. It is the docs half of the parity contract that
// internal/mcp.catalog_test holds up from the server side: whatever the server
// registers must be renderable, with a stable anchor, and a parameter table that
// is not silently empty.
func TestGenerateMCPToolsCoversCatalog(t *testing.T) {
	repoRoot(t)

	names := make([]string, 0, len(seamlessmcp.Catalog()))
	for _, tool := range seamlessmcp.Catalog() {
		names = append(names, tool.Name)
	}
	require.Len(t, names, seamlessmcp.ToolCount)

	md, err := generateMCPTools(&Page{Src: "reference/mcp/all.md", Tools: names}, "docs-src")
	require.NoError(t, err)

	for _, name := range names {
		require.Contains(t, md, "## "+name+" {#"+name+"}", "%s: heading with a pinned anchor", name)
	}
	require.NotContains(t, md, "<slug>", "generated text is HTML-escaped, or the browser eats it as a tag")
	require.Contains(t, md, "plan:&lt;slug&gt;")
}

func TestGenerateMCPToolsParamTable(t *testing.T) {
	repoRoot(t)

	md, err := generateMCPTools(&Page{Src: "reference/mcp/tasks.md", Tools: []string{"tasks_add"}}, "docs-src")
	require.NoError(t, err)

	require.Contains(t, md, "| Parameter | Type | Required | Description |")
	require.Contains(t, md, "| `title` | string | **yes** |", "required params are marked and come first")
	require.Contains(t, md, "| `plan` | string | no |")
	require.Less(t, strings.Index(md, "| `title` |"), strings.Index(md, "| `plan` |"),
		"required parameters sort before optional ones")
}

// TestGenerateMCPToolsRendersEnums: an enum's values are the most useful thing
// on the page, and their pipes are the likeliest way to shred a table row.
func TestGenerateMCPToolsRendersEnums(t *testing.T) {
	repoRoot(t)

	md, err := generateMCPTools(&Page{Src: "reference/mcp/tasks.md", Tools: []string{"tasks_update"}}, "docs-src")
	require.NoError(t, err)
	require.Contains(t, md, "One of: `open`, `in_progress`, `done`, `dropped`")
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "| `status`") {
			require.Equal(t, 5, strings.Count(line, "|")-strings.Count(line, "\\|"),
				"a table row must have exactly 4 columns; unescaped pipes add more")
		}
	}
}

func TestGenerateMCPToolsErrors(t *testing.T) {
	repoRoot(t)

	_, err := generateMCPTools(&Page{Src: "a.md"}, "docs-src")
	require.ErrorContains(t, err, "needs a `tools:` list")

	_, err = generateMCPTools(&Page{Src: "a.md", Tools: []string{"tasks_teleport"}}, "docs-src")
	require.ErrorContains(t, err, "no such MCP tool")
}

// TestGenerateConfigCoversExample is the config half of the same idea: every key
// the shipped example file sets must appear in the generated table, or the
// reference is lying about the surface.
func TestGenerateConfigCoversExample(t *testing.T) {
	repoRoot(t)

	md, err := generateConfig(&Page{Src: "reference/configuration.md"}, "docs-src")
	require.NoError(t, err)

	for _, key := range []string{
		"addr", "data_dir", "mcp.api_key",
		"budgets.max_briefing_tokens", "budgets.recall_budget_tokens",
		"briefing.findings_count", "briefing.hard_cap_multiplier",
		"llm.provider", "llm.openai.chat_model", "llm.ollama.base_url", "llm.anthropic.chat_model",
		"gardener.enabled", "gardener.session_idle_minutes",
		"capture.allowed_ports", "plan_capture.enabled",
	} {
		require.Contains(t, md, "| `"+key+"` |", "key %s is missing from the table", key)
	}

	require.Contains(t, md, "| `addr` | string | `127.0.0.1:8081` |")
	require.Contains(t, md, "| `mcp.api_key` | string | - |", "a key with no default says so")
	require.Contains(t, md, "| `capture.allowed_ports` | []int | `[80, 443]` |")
	require.Contains(t, md, "```yaml", "the example file ships verbatim")
	require.NotContains(t, md, "| `sourcePath` |", "unexported bookkeeping is not a config key")
}

func TestGenerateUnknown(t *testing.T) {
	_, err := generate("nope", &Page{}, "docs-src")
	require.ErrorContains(t, err, "unknown generator (known: config, mcp-tools)")
}

func TestEscapeCell(t *testing.T) {
	require.Equal(t, "startup\\|resume", escapeCell("startup|resume"))
	require.Equal(t, "plan:&lt;slug&gt;", escapeCell("plan:<slug>"))
	require.Equal(t, "a &amp; b", escapeCell("a & b"))
	require.Equal(t, "one two", escapeCell("one\ntwo"), "newlines would end the table early")
	// Code spans are escaped by the renderer, so pre-escaping would double-encode.
	require.Equal(t, "<slug>", escapeCodeCell("<slug>"))
	require.Equal(t, "a\\|b", escapeCodeCell("a|b"))
}
