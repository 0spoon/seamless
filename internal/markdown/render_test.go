package markdown

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// testResolver resolves the bare name "known" to a console memory link and
// everything else to not-found, mirroring the console's project-scoped lookup.
func testResolver(name string) (string, bool) {
	if name == "known" {
		return "/console/memories/ID123", true
	}
	return "", false
}

func TestRender_Structure(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		contains []string
	}{
		{"heading", "# Title", []string{"<h1>Title</h1>"}},
		{"emphasis", "**b** and _i_", []string{"<strong>b</strong>", "<em>i</em>"}},
		{"unordered list", "- one\n- two", []string{"<ul>", "<li>one</li>", "<li>two</li>"}},
		{"ordered list", "1. one\n2. two", []string{"<ol>", "<li>one</li>"}},
		{"inline code", "use `go test`", []string{"<code>go test</code>"}},
		{"fenced code", "```\nx := 1\n```", []string{"<pre><code>", "x := 1"}},
		{"gfm table", "| a | b |\n|---|---|\n| 1 | 2 |", []string{"<table>", "<th>a</th>", "<td>1</td>"}},
		{"gfm strikethrough", "~~gone~~", []string{"<del>gone</del>"}},
		{"blockquote", "> quoted", []string{"<blockquote>", "quoted"}},
		{"external link", "[site](https://example.com)", []string{`href="https://example.com"`, ">site</a>"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(Render(tc.body, testResolver))
			for _, want := range tc.contains {
				require.Contains(t, got, want)
			}
		})
	}
}

func TestRender_WikiLinks(t *testing.T) {
	t.Run("resolved", func(t *testing.T) {
		got := string(Render("see [[known]] here", testResolver))
		// bluemonday serializes the boolean attribute as data-peek="" -- the
		// drawer intercepts on the [data-peek] attribute selector regardless.
		require.Contains(t, got, `href="/console/memories/ID123"`)
		require.Contains(t, got, `data-peek`)
		require.Contains(t, got, `>known</a>`)
	})
	t.Run("resolved with alias and project qualifier", func(t *testing.T) {
		got := string(Render("ref [[proj/known|The Thing]] end", testResolver))
		require.Contains(t, got, `href="/console/memories/ID123"`)
		require.Contains(t, got, `>The Thing</a>`)
	})
	t.Run("unresolved stays literal", func(t *testing.T) {
		got := string(Render("see [[missing]] here", testResolver))
		require.Contains(t, got, "[[missing]]")
		require.NotContains(t, got, "<a ")
	})
	t.Run("nil resolver disables linking", func(t *testing.T) {
		got := string(Render("see [[known]] here", nil))
		require.Contains(t, got, "[[known]]")
		require.NotContains(t, got, "data-peek")
	})
	t.Run("brackets in code span stay literal", func(t *testing.T) {
		got := string(Render("`[[known]]`", testResolver))
		require.Contains(t, got, "<code>[[known]]</code>")
		require.NotContains(t, got, "data-peek")
	})
	t.Run("empty reference is left alone", func(t *testing.T) {
		got := string(Render("edge [[]] case", testResolver))
		require.NotContains(t, got, "data-peek")
	})
}

// TestRender_XSS is the safety battery: every executable vector must come out
// inert after the goldmark(WithUnsafe OFF) + bluemonday pass. Inert leftover
// text (e.g. the string "alert(1)") is harmless and not asserted against; only
// the executable constructs are.
func TestRender_XSS(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		resolve     WikiResolver
		notContains []string
	}{
		{"inline script tag", "hello <script>alert(1)</script>", testResolver, []string{"<script"}},
		{"raw img onerror", `<img src=x onerror="alert(1)">`, testResolver, []string{"onerror"}},
		{"javascript link", "[click](javascript:alert(1))", testResolver, []string{"javascript:"}},
		{"data-uri html link", "[x](data:text/html;base64,PHNjcmlwdD4=)", testResolver, []string{"data:text/html"}},
		{"raw anchor onclick", `<a href="/x" onclick="steal()">y</a>`, testResolver, []string{"onclick"}},
		{"resolver href is sanitized", "[[x]]", func(string) (string, bool) { return "javascript:alert(1)", true }, []string{"javascript:"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.ToLower(string(Render(tc.body, tc.resolve)))
			for _, bad := range tc.notContains {
				require.NotContains(t, got, strings.ToLower(bad))
			}
		})
	}
}

func TestRender_Empty(t *testing.T) {
	require.Equal(t, "", string(Render("", testResolver)))
	require.Equal(t, "", string(Render("   \n\t ", testResolver)))
}
