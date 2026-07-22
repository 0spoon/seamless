package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEveryPageHasAMarkdownTwin: the edge rewrite maps every /docs/.../ URL to
// its index.md unconditionally, so a page without a twin would 404 for any
// agent negotiating markdown. Scenario pages are outside the rewrite's scope
// and carry no twin.
func TestEveryPageHasAMarkdownTwin(t *testing.T) {
	repoRoot(t)

	files := renderRepoSite(t)
	twins := 0
	for name := range files {
		if strings.HasPrefix(name, "scenarios/") || !strings.HasSuffix(name, "index.html") {
			continue
		}
		twin := strings.TrimSuffix(name, "index.html") + "index.md"
		require.Contains(t, files, twin, "%s has no markdown twin", name)
		twins++
	}
	require.NotZero(t, twins)
}

// TestMarkdownTwinShape pins the twin's contract: the title and description
// lead, the body follows untruncated, root-absolute links become canonical
// URLs (the twin is served under a directory URL where relative resolution
// would land them at the wrong root), and a section index appends its card
// grid as a link list.
func TestMarkdownTwinShape(t *testing.T) {
	dir := writeSrc(t, map[string]string{
		"nav.yaml": `
sections:
  - title: Getting started
    slug: ""
    pages: [index.md, quickstart.md]
  - title: Reference
    slug: reference
    description: The surface.
    pages: [mcp.md]
`,
		"index.md":         page("Home", "start with [quickstart](/quickstart/)"),
		"quickstart.md":    "---\ntitle: Quickstart\ndescription: One command.\n---\n\nsee [mcp](/reference/mcp/) and [external](https://example.com/x)\n",
		"reference/mcp.md": page("MCP", "tools"),
	})
	site, err := loadSite(dir)
	require.NoError(t, err)
	require.NoError(t, renderPages(site))

	var quickstart, refIndex *Page
	for _, p := range site.Pages {
		switch p.URL {
		case "quickstart/":
			quickstart = p
		case "reference/":
			refIndex = p
		}
	}
	require.NotNil(t, quickstart)
	require.NotNil(t, refIndex)

	twin := string(markdownTwin(quickstart))
	require.True(t, strings.HasPrefix(twin, "# Quickstart\n\n> One command.\n\n"), twin)
	require.Contains(t, twin, "[mcp]("+siteBaseURL+"/docs/reference/mcp/)")
	require.Contains(t, twin, "[external](https://example.com/x)", "absolute URLs pass through untouched")

	// The generated section index has no body; its twin is the card list.
	grid := string(markdownTwin(refIndex))
	require.True(t, strings.HasPrefix(grid, "# Reference\n\n> The surface.\n"), grid)
	require.Contains(t, grid, "- [MCP]("+siteBaseURL+"/docs/reference/mcp/): ")
}

// TestAbsolutizeRootLinks covers the destination families rewriteDocLinks
// distinguishes: docs-root paths, site-root scenario paths, fragments,
// scheme-relative and already-relative destinations.
func TestAbsolutizeRootLinks(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[a](/concepts/memory/)", "[a](" + siteBaseURL + "/docs/concepts/memory/)"},
		{"[a](/quickstart/#install)", "[a](" + siteBaseURL + "/docs/quickstart/#install)"},
		{"[s](/scenarios/cold-start/)", "[s](" + siteBaseURL + "/scenarios/cold-start/)"},
		{"![i](/static/og.png)", "![i](" + siteBaseURL + "/docs/static/og.png)"},
		{"[p](//host/x)", "[p](//host/x)"},
		{"[r](sibling/)", "[r](sibling/)"},
		{"[h](https://example.com/)", "[h](https://example.com/)"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, absolutizeRootLinks(tc.in), tc.in)
	}
}
