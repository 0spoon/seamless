package console

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOverviewFrontDoor_RendersProductNarrativeAndSystemLayers(t *testing.T) {
	_, mux := newConsole(t)
	page := getPeek(t, mux, "/console/?w=7d")
	require.Equal(t, http.StatusOK, page.Code)
	body := page.Body.String()

	require.Contains(t, body, `class="overview-page" data-overview`)
	require.Contains(t, body, `class="overview-hero topbar"`, "the hero remains inside the live-refresh status contract")
	require.Contains(t, body, "Shared context,")
	require.Contains(t, body, "alive between agents.")
	require.Contains(t, body, `class="live overview-live"`)
	require.Contains(t, body, `class="active" aria-current="page" href="?w=7d"`)

	require.Contains(t, body, "est. tokens injected",
		"the reach vital pairs the reach rate with its injected-token cost")

	for _, id := range []string{
		"overview-circulation-title",
		"overview-atlas-title",
		"overview-workspaces-title",
		"overview-activity-title",
	} {
		require.Contains(t, body, `aria-labelledby="`+id+`"`)
		require.Contains(t, body, `id="`+id+`"`)
	}

	for _, href := range []string{
		`href="/console/retrieval"`,
		`href="/console/projects"`,
		`href="/console/memories"`,
		`href="/console/notes"`,
		`href="/console/sessions"`,
		`href="/console/tasks"`,
		`href="/console/gardener"`,
		`href="/console/interactions"`,
	} {
		require.Contains(t, body, href)
	}
}

func TestOverviewFrontDoor_StylesStayScopedAndStackResponsively(t *testing.T) {
	css := string(consoleCSS)
	overviewAt := strings.Index(css, "OVERVIEW FRONT DOOR")
	require.NotEqual(t, -1, overviewAt)
	overviewCSS := css[overviewAt:]
	require.Contains(t, overviewCSS, ".overview-hero.topbar {")
	require.Contains(t, overviewCSS, ".overview-vitals-grid {")
	require.Contains(t, overviewCSS, ".overview-workspace-grid {")
	require.Contains(t, overviewCSS, ".overview-activity-row {")

	stackAt := strings.Index(overviewCSS, "@media (max-width: 960px)")
	require.NotEqual(t, -1, stackAt)
	require.Contains(t, overviewCSS[stackAt:], ".overview-hero.topbar { grid-template-columns: 1fr; }")
	require.Contains(t, overviewCSS[stackAt:], ".overview-vitals-grid { grid-template-columns: 1fr; }")
}
