package console

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/store"
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

func TestOverviewFrontDoor_MishapRailAttributesAgentReports(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()

	// Before any report the rail is present as a positive all-clear.
	page := getPeek(t, mux, "/console/")
	require.Equal(t, http.StatusOK, page.Code)
	body := page.Body.String()
	require.Contains(t, body, "Agent-reported mishaps")
	require.Contains(t, body, "No mishaps reported")
	require.NotContains(t, body, "has-mishaps")

	// A report attributes to its session's harness + model and links back to it.
	sessID, err := core.NewID()
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: sessID, Name: "cc/mishapsess", ProjectSlug: "seamless",
		Status: core.SessionActive, Ambient: true, Model: "claude-fable-5",
		CreatedAt: now, UpdatedAt: now,
	}))
	evID, err := events.NewRecorder(db).Record(ctx, core.Event{
		Kind: core.EventAgentMishap, SessionID: sessID, ProjectSlug: "seamless",
		Payload: map[string]any{"description": "ran gofmt -w . across other agents' worktrees"},
	})
	require.NoError(t, err)

	page = getPeek(t, mux, "/console/")
	require.Equal(t, http.StatusOK, page.Code)
	body = page.Body.String()
	require.Contains(t, body, "has-mishaps", "the rail header warms once a report exists")
	require.Contains(t, body, "ran gofmt -w . across other agents&#39; worktrees")
	require.Contains(t, body, `data-href="/console/events/`+evID+`"`, "a row opens its event detail")
	require.Contains(t, body, `cc · fable-5`, "the agent pill names the reporting harness and model")
	require.Contains(t, body, `href="/console/sessions/`+sessID+`"`, "the report links to the exact session")
	require.NotContains(t, body, "No mishaps reported")
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
	require.Contains(t, overviewCSS, ".overview-mishap-row {")

	stackAt := strings.Index(overviewCSS, "@media (max-width: 960px)")
	require.NotEqual(t, -1, stackAt)
	require.Contains(t, overviewCSS[stackAt:], ".overview-hero.topbar { grid-template-columns: 1fr; }")
	require.Contains(t, overviewCSS[stackAt:], ".overview-vitals-grid { grid-template-columns: 1fr; }")
}
