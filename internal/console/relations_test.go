package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/store"
)

func TestRelations_AllProjectsTree(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.CreateProject(ctx, db, core.Project{
		ID: mustID(t), Slug: "seamless", Name: "Seamless", CreatedAt: now, UpdatedAt: now,
	}))
	sessID := mustID(t)
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: sessID, Name: "cc/s1", ProjectSlug: "seamless", Status: core.SessionActive,
		CreatedAt: now, UpdatedAt: now,
	}))
	step1 := mustID(t)
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: step1, ProjectSlug: "seamless", Title: "Lift store joins",
		Status: core.TaskOpen, PlanSlug: "demo", CreatedAt: now, UpdatedAt: now,
	}))
	_, err := store.ClaimTask(ctx, db, step1, sessID, 15*time.Minute, now)
	require.NoError(t, err)
	step2 := mustID(t)
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: step2, ProjectSlug: "seamless", Title: "Relations screen",
		Status: core.TaskOpen, PlanSlug: "demo", DependsOn: []string{step1},
		CreatedAt: now, UpdatedAt: now,
	}))
	insertRawMemory(t, db, mustID(t), "seamless", "cc/s1", now)

	body := getHTMLBody(t, mux, "/console/relations")
	require.Contains(t, body, "plan:demo", "the plan node")
	require.Contains(t, body, "Lift store joins", "step 1 title")
	require.Contains(t, body, "Relations screen", "step 2 title")
	require.Contains(t, body, "cc/s1", "the claiming session node")
	require.Contains(t, body, "blocked by Lift store joins", "the open step's blocked-by edge")
	require.Contains(t, body, `href="/console/relations?scope=project&amp;project=seamless"`,
		"all-projects mode links each section header to its single-project scope")
	require.Contains(t, body, "Seamless", "the project section header name")
}

func TestRelations_SingleProjectScope(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, slug := range []string{"alpha", "beta"} {
		require.NoError(t, store.CreateProject(ctx, db, core.Project{
			ID: mustID(t), Slug: slug, Name: slug, CreatedAt: now, UpdatedAt: now,
		}))
		require.NoError(t, store.CreateTask(ctx, db, core.Task{
			ID: mustID(t), ProjectSlug: slug, Title: slug + " work",
			Status: core.TaskOpen, PlanSlug: slug + "-plan", CreatedAt: now, UpdatedAt: now,
		}))
	}

	body := getHTMLBody(t, mux, "/console/relations?scope=project&project=alpha")
	require.Contains(t, body, "plan:alpha-plan", "the selected project's plan")
	require.NotContains(t, body, "plan:beta-plan", "single scope must not render other projects")
	// The selector marks the selected project active.
	require.Contains(t, body, `class="on" href="/console/relations?scope=project&amp;project=alpha"`)
}

func TestRelations_BadScopeIs400(t *testing.T) {
	_, mux := newConsole(t)
	req := httptest.NewRequest(http.MethodGet, "/console/relations?scope=bogus", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "all, project", "a bad scope must name the valid values")
}

func TestRelations_UnknownProjectIs404(t *testing.T) {
	_, mux := newConsole(t)
	req := httptest.NewRequest(http.MethodGet, "/console/relations?scope=project&project=nope", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), "nope", "a missing slug 404 must name the slug")
}

func TestRelations_ScopeProjectRequiresSlug(t *testing.T) {
	_, mux := newConsole(t)
	req := httptest.NewRequest(http.MethodGet, "/console/relations?scope=project", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "project=")
}

func TestRelations_SharedBriefingBanner(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, slug := range []string{"acme", "acme-ios"} {
		require.NoError(t, store.CreateProject(ctx, db, core.Project{
			ID: mustID(t), Slug: slug, Name: slug, CreatedAt: now, UpdatedAt: now,
		}))
	}
	require.NoError(t, store.SetProjectParent(ctx, db, "acme-ios", "acme", now))
	insertRawMemory(t, db, mustID(t), "acme", "cc/x", now)

	body := getHTMLBody(t, mux, "/console/relations")
	require.Contains(t, body, "Cross-project ties")
	require.Contains(t, body, "shares 1 active memory into")
	require.Contains(t, body, "acme-ios", "the child appears in the shared-briefing banner")
}

func TestRelations_RetiredProjectSplitLineage(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.CreateProject(ctx, db, core.Project{
		ID: mustID(t), Slug: "old-agent", Name: "old-agent", CreatedAt: now, UpdatedAt: now,
	}))
	// A plan so the retired project also gets a tree section (not just a banner).
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: mustID(t), ProjectSlug: "old-agent", Title: "legacy step",
		Status: core.TaskDone, PlanSlug: "legacy", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, store.RetireProject(ctx, db, "old-agent", now.Add(-21*24*time.Hour)))

	// Two memories moved out of the retired project on the split.
	rec := events.NewRecorder(db)
	for _, dst := range []string{"dest-ios", "dest-backend"} {
		_, err := rec.Record(ctx, core.Event{
			Kind: core.EventMemoryMoved, ProjectSlug: dst,
			Payload: map[string]any{"name": "m", "from": "old-agent", "to": dst, "by": "gardener"},
		})
		require.NoError(t, err)
	}

	// All-projects mode surfaces the split-lineage banner.
	body := getHTMLBody(t, mux, "/console/relations")
	require.Contains(t, body, "was split", "retired project shows a split-lineage banner")
	require.Contains(t, body, "dest-ios")
	require.Contains(t, body, "dest-backend")

	// A retired project is still reachable in single-project scope (not a 404).
	req := httptest.NewRequest(http.MethodGet, "/console/relations?scope=project&project=old-agent", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "retired", "the retired chip renders in single scope")
}

func TestRelations_SiblingFamilyBanner(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, slug := range []string{"web", "mobile"} {
		require.NoError(t, store.CreateProject(ctx, db, core.Project{
			ID: mustID(t), Slug: slug, Name: slug, CreatedAt: now, UpdatedAt: now,
		}))
	}
	require.NoError(t, store.SetProjectFamilies(ctx, db, map[string][]string{
		"clients": {"web", "mobile"},
	}))

	// Single-project scope surfaces the project's sibling-family tie.
	body := getHTMLBody(t, mux, "/console/relations?scope=project&project=web")
	require.Contains(t, body, "shares a briefing family with")
	require.Contains(t, body, "mobile")
}
