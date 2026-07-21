package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/store"
)

func contextProjectBySlug(t *testing.T, projects []contextProject, slug string) contextProject {
	t.Helper()
	for _, project := range projects {
		if project.Slug == slug {
			return project
		}
	}
	t.Fatalf("no context project %q in %#v", slug, projects)
	return contextProject{}
}

func contextFlowByKind(t *testing.T, flows []contextFlow, kind string) contextFlow {
	t.Helper()
	for _, flow := range flows {
		if flow.Kind == kind {
			return flow
		}
	}
	t.Fatalf("no %q flow in %#v", kind, flows)
	return contextFlow{}
}

func TestContext_AllScopesShowsEffectiveBriefingTopology(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, slug := range []string{"acme", "acme-ios", "acme-web"} {
		require.NoError(t, store.CreateProject(ctx, db, core.Project{
			ID: mustID(t), Slug: slug, Name: slug, CreatedAt: now, UpdatedAt: now,
		}))
	}
	require.NoError(t, store.SetProjectParent(ctx, db, "acme-ios", "acme", now))
	require.NoError(t, store.SetProjectFamilies(ctx, db, map[string][]string{
		"clients": {"acme-ios", "acme-web"},
	}))
	require.NoError(t, store.SetBriefingConfig(ctx, db, config.Defaults().Briefing))
	insertRawMemory(t, db, mustID(t), "", "cc/global", now)
	insertRawMemory(t, db, mustID(t), "acme", "cc/parent", now)
	insertRawMemory(t, db, mustID(t), "acme-ios", "cc/ios", now)

	// Execution data exists, but Context must not resurrect the old plan/task
	// projection that belongs to Plans & tasks.
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: mustID(t), ProjectSlug: "acme-ios", Title: "context-must-not-render-this-task",
		Status: core.TaskOpen, PlanSlug: "old-relations-tree", CreatedAt: now, UpdatedAt: now,
	}))

	var data contextData
	getJSON(t, mux, "/console/context?format=json", &data)
	require.Equal(t, "all", data.Scope)
	require.Equal(t, 3, data.Summary.Scopes)
	require.Equal(t, 1, data.Summary.ParentLinks)
	require.Equal(t, 1, data.Summary.Families)
	require.Equal(t, 1, data.Rules.GlobalMemories)
	require.True(t, data.Rules.ParentEnabled)
	require.Equal(t, 2, data.Rules.SiblingFindings)
	require.False(t, data.Rules.SiblingMemories)

	ios := contextProjectBySlug(t, data.Projects, "acme-ios")
	require.Equal(t, 1, ios.LocalMemories)
	require.Equal(t, 1, ios.ParentMemories)
	require.True(t, contextFlowByKind(t, ios.Incoming, "global").Enabled)
	parent := contextFlowByKind(t, ios.Incoming, "parent")
	require.True(t, parent.Enabled)
	require.Contains(t, parent.Detail, "1 active memory")
	family := contextFlowByKind(t, ios.Incoming, "family")
	require.True(t, family.Enabled)
	require.Contains(t, family.Detail, "acme-web")
	require.Contains(t, family.Detail, "sibling memories are off")

	body := getHTMLBody(t, mux, "/console/context")
	require.Contains(t, body, "Briefing topology")
	require.Contains(t, body, "Global memory")
	require.Contains(t, body, "Briefing families")
	require.Contains(t, body, `/console/settings#briefing-recipe`)
	require.NotContains(t, body, "context-must-not-render-this-task")
	require.NotContains(t, body, "old-relations-tree")
}

func TestContext_FocusedScopeAndDisabledChannels(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, slug := range []string{"shared", "ios", "android"} {
		require.NoError(t, store.CreateProject(ctx, db, core.Project{
			ID: mustID(t), Slug: slug, Name: slug, CreatedAt: now, UpdatedAt: now,
		}))
	}
	require.NoError(t, store.SetProjectParent(ctx, db, "ios", "shared", now))
	require.NoError(t, store.SetProjectFamilies(ctx, db, map[string][]string{
		"mobile": {"ios", "android"},
	}))
	require.NoError(t, store.SetBriefingConfig(ctx, db, config.Briefing{}))

	var data contextData
	getJSON(t, mux, "/console/context?format=json&scope=project&project=ios", &data)
	require.Equal(t, "project", data.Scope)
	require.Equal(t, "ios", data.Project)
	require.Equal(t, 1, data.Summary.Scopes)
	require.Equal(t, 1, data.Summary.ParentLinks)
	require.Equal(t, 1, data.Summary.Families)
	require.Len(t, data.Projects, 1)
	require.Equal(t, "ios", data.Projects[0].Slug)
	require.Len(t, data.Families, 1)
	require.False(t, data.Rules.ParentEnabled)
	require.Zero(t, data.Rules.SiblingFindings)
	require.False(t, data.Rules.SiblingMemories)
	require.False(t, contextFlowByKind(t, data.Projects[0].Incoming, "parent").Enabled)
	require.False(t, contextFlowByKind(t, data.Projects[0].Incoming, "family").Enabled)
	require.Contains(t, data.Families[0].Warnings, "Both sibling channels are disabled in briefing settings.")

	body := getHTMLBody(t, mux, "/console/context?scope=project&project=ios")
	require.Contains(t, body, "What ios receives and shares")
	require.Contains(t, body, "Parent-memory inheritance is disabled")
	require.Contains(t, body, "both sibling briefing channels are off")
}

func TestContext_SplitLineageUsesMoveHistory(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, slug := range []string{"old-app", "ios", "backend"} {
		require.NoError(t, store.CreateProject(ctx, db, core.Project{
			ID: mustID(t), Slug: slug, Name: slug, CreatedAt: now, UpdatedAt: now,
		}))
	}
	require.NoError(t, store.RetireProject(ctx, db, "old-app", now.Add(-21*24*time.Hour)))
	recorder := events.NewRecorder(db)
	for _, destination := range []string{"ios", "ios", "backend", "vanished"} {
		_, err := recorder.Record(ctx, core.Event{
			Kind: core.EventMemoryMoved, ProjectSlug: destination,
			Payload: map[string]any{"name": "m", "from": "old-app", "to": destination, "by": "gardener"},
		})
		require.NoError(t, err)
	}
	_, err := recorder.Record(ctx, core.Event{
		Kind:    core.EventMemoryMoved,
		Payload: map[string]any{"name": "global-m", "from": "old-app", "to": "", "by": "gardener"},
	})
	require.NoError(t, err)

	var data contextData
	getJSON(t, mux, "/console/context?format=json", &data)
	require.Equal(t, 1, data.Summary.Lineages)
	require.Equal(t, 1, data.Summary.Warnings)
	require.Len(t, data.Lineages, 1)
	require.Equal(t, "old-app", data.Lineages[0].Source.Slug)
	require.Equal(t, 5, data.Lineages[0].Moves)
	require.Len(t, data.Lineages[0].Destinations, 4)
	require.True(t, data.Lineages[0].Destinations[0].Scope.Global)
	require.Equal(t, "global", data.Lineages[0].Destinations[0].Scope.Slug)
	require.Equal(t, "backend", data.Lineages[0].Destinations[1].Scope.Slug)
	require.Equal(t, 1, data.Lineages[0].Destinations[1].Moves)
	require.Equal(t, "ios", data.Lineages[0].Destinations[2].Scope.Slug)
	require.Equal(t, 2, data.Lineages[0].Destinations[2].Moves)
	require.Equal(t, "vanished", data.Lineages[0].Destinations[3].Scope.Slug)
	require.Empty(t, data.Lineages[0].Destinations[3].Scope.FocusHref,
		"historical destinations absent from the current topology must not produce dead links")
	require.Contains(t, getHTMLBody(t, mux, "/console/context"), "scope no longer known")
	require.Contains(t, getHTMLBody(t, mux, "/console/context"), "shared pool")

	// Focusing a destination keeps the historical edge that reaches it.
	getJSON(t, mux, "/console/context?format=json&scope=project&project=ios", &data)
	require.Len(t, data.Lineages, 1)
	require.Contains(t, getHTMLBody(t, mux, "/console/context?scope=project&project=ios"), "Split lineage")
}

func TestContext_SplitLineagePagesTheFullMoveHistory(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, slug := range []string{"old-app", "new-app"} {
		require.NoError(t, store.CreateProject(ctx, db, core.Project{
			ID: mustID(t), Slug: slug, Name: slug, CreatedAt: now, UpdatedAt: now,
		}))
	}
	require.NoError(t, store.RetireProject(ctx, db, "old-app", now.Add(-24*time.Hour)))
	recorder := events.NewRecorder(db)
	for range 501 {
		_, err := recorder.Record(ctx, core.Event{
			Kind:    core.EventMemoryMoved,
			Payload: map[string]any{"name": "m", "from": "old-app", "to": "new-app", "by": "gardener"},
		})
		require.NoError(t, err)
	}

	var data contextData
	getJSON(t, mux, "/console/context?format=json&scope=project&project=old-app", &data)
	require.Len(t, data.Lineages, 1)
	require.Equal(t, 501, data.Lineages[0].Moves,
		"lineage must continue past the event reader's 500-row page size")
}

func TestContext_StrictScopeValidation(t *testing.T) {
	_, mux := newConsole(t)
	cases := []struct {
		path   string
		status int
		wants  string
	}{
		{"/console/context?scope=bogus", http.StatusBadRequest, "all, project"},
		{"/console/context?scope=", http.StatusBadRequest, "invalid scope"},
		{"/console/context?scope=project", http.StatusBadRequest, "project="},
		{"/console/context?project=demo", http.StatusBadRequest, "requires scope=project"},
		{"/console/context?scope=all&scope=project", http.StatusBadRequest, "exactly once"},
		{"/console/context?scpoe=project", http.StatusBadRequest, "invalid parameter"},
		{"/console/context?format=html", http.StatusBadRequest, "valid value is json"},
		{"/console/context?scope=project&project=nope", http.StatusNotFound, "nope"},
	}
	for _, test := range cases {
		req := httptest.NewRequest(http.MethodGet, test.path, nil)
		req.Header.Set("Authorization", "Bearer "+testKey)
		rr := do(mux, req)
		require.Equal(t, test.status, rr.Code, "GET %s", test.path)
		require.Contains(t, rr.Body.String(), test.wants, "GET %s", test.path)
	}
}

func TestContext_LegacyRelationsURLRedirects(t *testing.T) {
	_, mux := newConsole(t)
	req := httptest.NewRequest(http.MethodGet, "/console/relations?scope=project&project=demo", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusPermanentRedirect, rr.Code)
	require.Equal(t, "/console/context?scope=project&project=demo", rr.Header().Get("Location"))
}

func TestContext_TemplateHasResponsiveTopologyLayout(t *testing.T) {
	source, err := templateFS.ReadFile("templates/context_shared.html")
	require.NoError(t, err)
	require.Contains(t, string(source), `class="context-rule-grid"`)
	require.Contains(t, string(source), `class="context-flow-columns"`)
	require.Contains(t, string(source), `class="context-lineage-list"`)

	css := string(consoleCSS)
	require.Contains(t, css, ".context-scope-grid")
	require.Contains(t, css, "@media (max-width: 760px)")
	require.Contains(t, css, ".context-flow-columns { grid-template-columns: 1fr; }")
}
