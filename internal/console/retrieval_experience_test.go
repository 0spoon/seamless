package console

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetrievalCirculationReport_RendersNarrativeAndWindowState(t *testing.T) {
	_, mux := newConsole(t)
	page := getPeek(t, mux, "/console/retrieval?w=7d")
	require.Equal(t, http.StatusOK, page.Code)
	body := page.Body.String()

	require.Contains(t, body, `class="retrieval-page" data-retrieval`)
	require.Contains(t, body, `class="retrieval-hero"`)
	require.Contains(t, body, "Context circulation")
	require.Contains(t, body, "est. tokens injected")
	require.Contains(t, body, `class="active" aria-current="page" href="/console/retrieval?w=7d"`)

	for _, id := range []string{
		"retrieval-delivery-title",
		"retrieval-pattern-title",
		"retrieval-scopes-title",
		"retrieval-pressure-title",
	} {
		require.Contains(t, body, `aria-labelledby="`+id+`"`)
		require.Contains(t, body, `id="`+id+`"`)
	}

	for _, class := range []string{
		`class="retrieval-flow-panel"`,
		`class="retrieval-analysis-grid"`,
		`class="retrieval-wide-empty"`,
		`class="retrieval-pressure-grid"`,
		`class="retrieval-panel retrieval-stale-panel healthy"`,
	} {
		require.Contains(t, body, class)
	}
}

func TestRetrievalCirculationReport_StylesStayScopedAndResponsive(t *testing.T) {
	css := string(consoleCSS)
	retrievalAt := strings.Index(css, "RETRIEVAL CIRCULATION REPORT")
	require.NotEqual(t, -1, retrievalAt)
	retrievalCSS := css[retrievalAt:]

	for _, selector := range []string{
		".retrieval-hero {",
		".retrieval-windowbar {",
		".retrieval-flow {",
		".retrieval-analysis-grid {",
		".retrieval-scope-row {",
		".retrieval-pressure-grid {",
	} {
		require.Contains(t, retrievalCSS, selector)
	}

	stackAt := strings.Index(retrievalCSS, "@media (max-width: 1080px)")
	require.NotEqual(t, -1, stackAt)
	require.Contains(t, retrievalCSS[stackAt:], ".retrieval-hero { grid-template-columns: 1fr; }")
	require.Contains(t, retrievalCSS[stackAt:], ".retrieval-analysis-grid, .retrieval-pressure-grid { grid-template-columns: 1fr; }")

	phoneAt := strings.Index(retrievalCSS, "@media (max-width: 540px)")
	require.NotEqual(t, -1, phoneAt)
	require.Contains(t, retrievalCSS[phoneAt:], ".retrieval-window-tabs { width: 100%;")
	require.Contains(t, retrievalCSS[phoneAt:], ".retrieval-scope-row { grid-template-columns: 1fr;")
}
