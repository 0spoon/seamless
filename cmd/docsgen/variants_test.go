package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestExpandVariants pins the emitted shape: one HTML block for the opener
// (div plus chip label, no blank line between them), a blank line so the
// contained markdown renders as markdown, and values re-ordered canonically
// no matter how the author listed them.
func TestExpandVariants(t *testing.T) {
	md := "intro\n\n" +
		"::: when os=windows,macos client=codex\n" +
		"run `irm`\n" +
		":::\n\nafter\n"
	out, has, err := expandVariants(md)
	require.NoError(t, err)
	require.True(t, has)
	require.Contains(t, out, `<div class="ctx-variant" data-ctx-os="macos windows" data-ctx-client="codex">`)
	require.Contains(t, out, `<p class="ctx-label"><span class="ctx-chip">macOS</span><span class="ctx-chip">Windows</span><span class="ctx-chip">Codex</span></p>`)
	require.Contains(t, out, "</p>\n\nrun `irm`\n\n</div>\n\nafter")

	plain := "no containers here\n"
	same, has, err := expandVariants(plain)
	require.NoError(t, err)
	require.False(t, has)
	require.Equal(t, plain, same)
}

// TestExpandVariantsFenceAware: a literal `::: when` inside a code fence is
// content, not a container.
func TestExpandVariantsFenceAware(t *testing.T) {
	md := "```text\n::: when os=windows\n:::\n```\n"
	out, has, err := expandVariants(md)
	require.NoError(t, err)
	require.False(t, has)
	require.Equal(t, md, out)
}

func TestExpandVariantsErrors(t *testing.T) {
	cases := []struct{ name, md, wantErr string }{
		{"unknown os value", "::: when os=beos\nx\n:::\n", `unknown os value "beos"`},
		{"unknown dimension", "::: when arch=arm64\nx\n:::\n", `unknown dimension "arch"`},
		{"nested", "::: when os=linux\n::: when os=macos\nx\n:::\n:::\n", "cannot nest"},
		{"unclosed", "::: when os=linux\nx\n", "never closed"},
		{"stray closer", "text\n:::\n", "no open variant container"},
		{"heading inside", "::: when os=linux\n\n## Bad heading\n\n:::\n", "heading inside a variant container"},
		{"duplicate dimension", "::: when os=linux os=macos\nx\n:::\n", `dimension "os" given twice`},
		{"no conditions", "::: when\nx\n:::\n", "malformed variant marker"},
		{"malformed value list", "::: when os\nx\n:::\n", "malformed condition"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := expandVariants(tc.md)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestTextifyVariants: the markdown representations replace the opener with
// its bold label line, drop the closer, and keep the content -- every
// variant present and labeled, never hidden, never marker syntax.
func TestTextifyVariants(t *testing.T) {
	md := "a\n\n::: when os=windows client=codex\nbody line\n:::\n\nb\n"
	require.Equal(t, "a\n\n**Windows · Codex:**\nbody line\n\nb\n", textifyVariants(md))

	fenced := "```\n::: when os=windows\n```\n"
	require.Equal(t, fenced, textifyVariants(fenced))
}

// TestCtxBarPlacement: the bar is point-of-use chrome. On a variant page it
// renders below the h1 (demoted, not a banner); on a page without variants it
// does not render at all -- the header chip is the only picker chrome there.
func TestCtxBarPlacement(t *testing.T) {
	repoRoot(t)

	files := renderRepoSite(t)
	qs, ok := files["quickstart/index.html"]
	require.True(t, ok, "the quickstart page is emitted")
	require.Contains(t, qs, `class="ctx-bar"`)
	require.Greater(t, strings.Index(qs, `class="ctx-bar"`), strings.Index(qs, "</h1>"),
		"the bar renders after the page heading")

	concept, ok := files["concepts/how-it-works/index.html"]
	require.True(t, ok, "the concept page is emitted")
	require.NotContains(t, concept, `class="ctx-bar"`)
	require.Contains(t, concept, `class="ctx-chip-btn"`, "the header chip renders on every docs page")
}

// TestVariantPageEndToEnd: a page authored with a container renders the
// picker-visible div with its chips and real inner markdown, gates the bar
// via HasVariants, and keeps its twin and search text clean of syntax.
func TestVariantPageEndToEnd(t *testing.T) {
	dir := writeSrc(t, map[string]string{
		"nav.yaml": `
sections:
  - title: Getting started
    slug: ""
    pages: [index.md, setup.md]
`,
		"index.md": page("Home", "hello"),
		"setup.md": "---\ntitle: Setup\n---\n\nintro\n\n::: when os=windows\nwindows-only step\n:::\n",
	})
	site, err := loadSite(dir)
	require.NoError(t, err)
	require.NoError(t, renderPages(site))

	var home, setup *Page
	for _, p := range site.Pages {
		switch p.URL {
		case "":
			home = p
		case "setup/":
			setup = p
		}
	}
	require.NotNil(t, home)
	require.NotNil(t, setup)

	require.False(t, home.HasVariants, "a page without containers keeps its layout chrome-free")
	require.True(t, setup.HasVariants)
	body := string(setup.Body)
	require.Contains(t, body, `data-ctx-os="windows"`)
	require.Contains(t, body, `<span class="ctx-chip">Windows</span>`)
	require.Contains(t, body, "<p>windows-only step</p>", "inner markdown renders as markdown")

	twin := string(markdownTwin(setup))
	require.Contains(t, twin, "**Windows:**")
	require.NotContains(t, twin, ":::")

	require.Contains(t, setup.Text, "windows-only step")
	require.False(t, strings.Contains(setup.Text, ":::"), "search text carries no marker syntax")
}
