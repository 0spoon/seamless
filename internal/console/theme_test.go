package console

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The semantic palette is a contract across themes: a badge, a match highlight,
// or a live dot may change lightness between light and dark, never hue. These
// assertions guard the two ways that contract has broken in practice -- a raw
// hex escaping into markup, and a screen giving itself a dark-theme-only look.
func TestTheme_SemanticColorsAreLockedAcrossThemes(t *testing.T) {
	css := string(consoleCSS)

	// Exactly one dark block, and it only reassigns tokens. A screen-scoped
	// [data-theme="dark"] rule is how a page acquires a second personality.
	darkRules := regexp.MustCompile(`\[data-theme="dark"\][^{,]*\{`).FindAllString(css, -1)
	for _, rule := range darkRules {
		trimmed := strings.TrimSpace(strings.TrimSuffix(rule, "{"))
		require.Contains(t, []string{`[data-theme="dark"]`, `[data-theme="dark"] .tt-ico.ico-light`, `[data-theme="dark"] .tt-ico.ico-dark`},
			trimmed, "unexpected dark-theme override %q: restyle with tokens instead", trimmed)
	}

	// Both themes define the same semantic token set, so nothing falls back to a
	// light-theme value on a dark surface.
	dark := css[strings.Index(css, `[data-theme="dark"] {`):]
	dark = dark[:strings.Index(dark, "}")]
	for _, token := range []string{"--match", "--ok", "--warn", "--danger", "--brand", "--pop"} {
		require.Contains(t, dark, token+":", "the dark theme must restate %s", token)
	}

	// Search is the screen that drifted: it wore a coral wash that made the whole
	// page read warm in the dark theme while every other screen read indigo. The
	// hero that carried the wash is retired -- Search opens with the shared
	// compact title bar -- and the query chrome that remains stays on brand.
	require.NotContains(t, css, ".search-hero", "the search hero was retired for the shared compact title bar")
	queryAt := strings.Index(css, ".search.search-query {")
	require.NotEqual(t, -1, queryAt)
	query := css[queryAt : queryAt+strings.Index(css[queryAt:], "}")]
	require.NotContains(t, query, "--pop", "the search query chrome stays on the brand hue in both themes")
	require.NotContains(t, css, ".search-time-pills a.active { color: var(--pop-strong)")
}

// Color belongs to the stylesheet's tokens. A hex literal in markup cannot
// respond to the theme at all, so it is the one drift no override can fix.
func TestTemplates_CarryNoHexLiterals(t *testing.T) {
	hex := regexp.MustCompile(`(?:background|color|fill|stroke|border)\s*[:=]\s*["']?#[0-9a-fA-F]{3,8}`)
	entries, err := templateFS.ReadDir("templates")
	require.NoError(t, err)
	for _, e := range entries {
		b, rerr := templateFS.ReadFile("templates/" + e.Name())
		require.NoError(t, rerr)
		require.Empty(t, hex.FindAllString(string(b), -1), "templates/%s must use CSS tokens, not hex", e.Name())
	}
}

// The sidebar footer is one account row, not two floating buttons. The theme
// control keeps its id (the layout script binds to it) and the logout form
// keeps its action (it is the only way out), so the visual change cannot break
// either behavior.
func TestSidebar_IsOneAccountRow(t *testing.T) {
	_, mux := newConsole(t)
	body := getPeek(t, mux, "/console/").Body.String()

	require.Contains(t, body, `class="account"`)
	require.Contains(t, body, `class="account-dot"`)
	require.Contains(t, body, `id="theme-toggle"`, "the layout script binds by id")
	require.Contains(t, body, `action="/console/logout"`, "the logout form action is unchanged")
	require.Contains(t, body, `aria-label="Sign out"`, "an icon-only button still names itself")
	require.NotContains(t, body, `<span>Sign out</span>`, "the row is icons, not stacked labels")

	// The state that used to live in visible text ("Light theme" / "Dark theme")
	// resized the footer on every flip. It moved to the label.
	require.NotContains(t, body, `class="tt-label"`)
	css := string(consoleCSS)
	require.Contains(t, css, ".account-act {")
	require.Contains(t, css, "width: 26px; height: 26px;")
}

// "brand" names two things: the wordmark (<a class="brand"> in the sidebar and
// the phone bar) and the tone class on chips, badges, and icon tiles
// (.kind.brand, .badge.brand, ...). A bare .brand selector styles both -- the
// wordmark's flex: 1 once stretched every brand-toned chip across its row -- so
// every compound that names .brand must qualify it: a.brand for the wordmark,
// the component class for a tone.
func TestBrandClass_WordmarkRulesStayOnTheWordmark(t *testing.T) {
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(consoleCSS), "")
	// .brand alone in its compound: opened by a line start, combinator, comma,
	// or paren, and closed by a combinator, comma, paren, brace, or pseudo.
	bare := regexp.MustCompile(`(?m)(?:^|[\s,>+~(])\.brand[\s,>+~){:]`)
	require.Empty(t, bare.FindAllString(css, -1), "qualify .brand: a.brand for the wordmark, .<component>.brand for a tone")

	// The collapsed rail is a sidebar state. Its wordmark rules must not reach
	// the phone bar's wordmark, which stays centered whatever the rail does.
	require.Contains(t, css, `:root[data-sidebar="collapsed"] .sidebar a.brand {`)

	// The stream indicator lights every wordmark's orb: the phone bar's comes
	// first in the document and is hidden on desktop, so lighting only the first
	// match left the visible sidebar orb dark.
	layout, err := templateFS.ReadFile("templates/layout.html")
	require.NoError(t, err)
	require.Contains(t, string(layout), `document.querySelectorAll('a.brand .dot')`)
}

// System (no stored choice) follows the OS: a page carries no data-theme until
// the owner picks Light or Dark, and the stylesheet's prefers-color-scheme
// block does the rest before first paint. That block must be the dark theme
// exactly -- the same tokens with the same values, and nothing but tokens -- or
// "System on a dark Mac" and "Dark" would be two different consoles.
func TestTheme_SystemFollowsTheOS(t *testing.T) {
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(consoleCSS), "")
	block := func(opener string) string {
		at := strings.Index(css, opener)
		require.NotEqual(t, -1, at, "missing %q", opener)
		open := at + len(opener)
		depth := 1
		for i := open; i < len(css); i++ {
			switch css[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return css[open:i]
				}
			}
		}
		t.Fatalf("unclosed %q", opener)
		return ""
	}
	decl := regexp.MustCompile(`(--[\w-]+)\s*:\s*([^;]+);`)
	tokens := func(body string) map[string]string {
		out := map[string]string{}
		for _, m := range decl.FindAllStringSubmatch(body, -1) {
			out[m[1]] = strings.Join(strings.Fields(strings.ReplaceAll(m[2], ", ", ",")), " ")
		}
		return out
	}

	media := block(`@media (prefers-color-scheme: dark) {`)
	rules := regexp.MustCompile(`([^{}]+)\{`).FindAllStringSubmatch(media, -1)
	require.Len(t, rules, 1, "the OS-dark block holds one rule")
	require.Equal(t, `:root:not([data-theme="light"])`, strings.TrimSpace(rules[0][1]))
	inner := block(`:root:not([data-theme="light"]) {`)
	require.Empty(t, strings.TrimSpace(decl.ReplaceAllString(inner, "")), "the OS-dark block redefines tokens only")
	require.Equal(t, tokens(block(`[data-theme="dark"] {`)), tokens(inner), "System-dark is the Dark theme, token for token")

	// Under System the toggle offers the theme opposite the OS's.
	require.Contains(t, css, `@media (prefers-color-scheme: dark) { :root:not([data-theme]) .tt-ico.ico-light { display: inline-flex; } }`)
	require.Contains(t, css, `@media (prefers-color-scheme: light) { :root:not([data-theme]) .tt-ico.ico-dark { display: inline-flex; } }`)

	// Both pre-paint scripts set data-theme only for an explicit choice: no
	// default path writes one, so no stored choice means the OS decides.
	for _, name := range []string{"layout.html", "login.html"} {
		raw, err := templateFS.ReadFile("templates/" + name)
		require.NoError(t, err)
		src := string(raw)
		require.Contains(t, src, `if(t==='dark'||t==='light')`, name)
		require.NotContains(t, src, `||'dark'`, "%s: no default theme", name)
		require.NotContains(t, src, `setAttribute('data-theme','dark')`, "%s: no forced dark", name)
	}
	_, mux := newConsole(t)
	page := getPeek(t, mux, "/console/").Body.String()
	require.Contains(t, page, `<html lang="en">`, "the server renders no theme")
	require.NotContains(t, page, `||'dark'`)

	// Experience offers the choice, System first.
	settings := getPeek(t, mux, "/console/settings?s=experience").Body.String()
	radios := regexp.MustCompile(`name="theme" value="(\w+)"`).FindAllStringSubmatch(settings, -1)
	var values []string
	for _, m := range radios {
		values = append(values, m[1])
	}
	require.Equal(t, []string{"system", "light", "dark"}, values)
}
