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
