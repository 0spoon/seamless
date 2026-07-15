package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderMarkdownHeadings(t *testing.T) {
	out, err := renderMarkdown(`
Intro paragraph.

## How claiming works

text

### A sub heading

more

#### Too deep for the rail

## tasks_claim {#tasks_claim}

text
`, "")
	require.NoError(t, err)

	require.Equal(t, []Heading{
		{Level: 2, ID: "how-claiming-works", Text: "How claiming works"},
		{Level: 3, ID: "a-sub-heading", Text: "A sub heading"},
		{Level: 2, ID: "tasks_claim", Text: "tasks_claim"},
	}, out.Headings, "the rail carries h2/h3 only, in document order")

	// The rail's anchors must be the ones the body actually emits, or every TOC
	// link is a dead jump.
	require.Contains(t, string(out.HTML), `id="how-claiming-works"`)
	require.Contains(t, string(out.HTML), `id="tasks_claim"`)
}

// TestRenderMarkdownExplicitHeadingID is the anchor-stability contract: the
// auto-id algorithm rewrites tasks_claim to "tasks-claim", so generated pages
// pin ids explicitly. Published anchors are a promise; this is what keeps it.
func TestRenderMarkdownExplicitHeadingID(t *testing.T) {
	auto, err := renderMarkdown("## tasks_claim\n", "")
	require.NoError(t, err)
	require.Equal(t, "tasks-claim", auto.Headings[0].ID, "auto ids mangle underscores (the reason we pin them)")

	pinned, err := renderMarkdown("## tasks_claim {#tasks_claim}\n", "")
	require.NoError(t, err)
	require.Equal(t, "tasks_claim", pinned.Headings[0].ID)
}

func TestRenderMarkdownChromaClasses(t *testing.T) {
	out, err := renderMarkdown("```go\nfunc main() { // hi\n}\n```\n", "")
	require.NoError(t, err)
	html := string(out.HTML)
	require.Contains(t, html, `<span class="kd">func</span>`, "keywords are highlighted at build time")
	require.Contains(t, html, `class="c1"`, "comments carry a chroma class")
	require.Contains(t, html, `class="nf"`, "function names carry a chroma class")
	require.NotContains(t, html, "<style", "highlighting uses classes, not inline styles")
}

func TestRenderMarkdownGFMTable(t *testing.T) {
	out, err := renderMarkdown("| A | B |\n|---|---|\n| 1 | 2 |\n", "")
	require.NoError(t, err)
	require.Contains(t, string(out.HTML), "<table>")
}

// TestRenderMarkdownAllowsRawHTML documents the trust boundary: docs-src is
// repo-authored, so raw HTML passes through. internal/markdown.Render, which
// handles agent-authored content from the store, must keep doing the opposite.
func TestRenderMarkdownAllowsRawHTML(t *testing.T) {
	out, err := renderMarkdown(`<div class="sketch">hi</div>`, "")
	require.NoError(t, err)
	require.Contains(t, string(out.HTML), `<div class="sketch">`)
}

// TestRewriteDocLinks: authors write one root-absolute path per link and the
// depth arithmetic happens at build time, per page.
func TestRewriteDocLinks(t *testing.T) {
	const src = `[memory](/concepts/memory/) [anchor](/reference/mcp/tasks/#tasks_claim)
[external](https://example.com/x) [scheme-rel](//example.com/x)
[relative](quickstart/) [frag](#top) ![shot](/static/shot.png)`

	deep, err := renderMarkdown(src, "../../")
	require.NoError(t, err)
	require.Contains(t, string(deep.HTML), `href="../../concepts/memory/"`)
	require.Contains(t, string(deep.HTML), `href="../../reference/mcp/tasks/#tasks_claim"`)
	require.Contains(t, string(deep.HTML), `src="../../static/shot.png"`)

	home, err := renderMarkdown(src, "")
	require.NoError(t, err)
	require.Contains(t, string(home.HTML), `href="concepts/memory/"`)

	// Everything that is not a same-site absolute path is left alone.
	for _, untouched := range []string{
		`href="https://example.com/x"`,
		`href="//example.com/x"`,
		`href="quickstart/"`,
		`href="#top"`,
	} {
		require.Contains(t, string(deep.HTML), untouched)
	}

	// Only the rewritten same-site paths are reported for link checking.
	require.Equal(t, []string{
		"/concepts/memory/",
		"/reference/mcp/tasks/#tasks_claim",
		"/static/shot.png",
	}, deep.Links)
}

func TestPlainTextCaps(t *testing.T) {
	long := strings.Repeat("word ", 1000)
	require.Len(t, []rune(plainText(long)), searchTextRunes)
	require.Equal(t, "hello world", plainText("# hello\n\nworld"))
}
