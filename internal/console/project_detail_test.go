package console

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// insertRawMemory seeds a memory index row directly (the console test harness
// has no Files layer to index through), mirroring files.IndexMemory's columns.
func insertRawMemory(t *testing.T, db *sql.DB, id, project, sourceSession string, ts time.Time) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO memories_index
		    (id, kind, name, description, project, file_path, tags,
		     valid_from, invalid_at, superseded_by, source_session,
		     content_hash, created_at, updated_at)
		VALUES (?, 'gotcha', ?, 'a seeded memory', ?, ?, '[]', ?, NULL, NULL, ?, '', ?, ?)`,
		id, id, project, id+".md", core.FormatTime(ts), sourceSession,
		core.FormatTime(ts), core.FormatTime(ts))
	require.NoError(t, err)
}

// getHTMLBody issues an authenticated browser GET (no JSON) and returns the body
// after asserting a 200 -- so a template execution error (which renders 500)
// fails the test.
func getHTMLBody(t *testing.T, mux *http.ServeMux, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code, "GET %s -> %d: %s", path, rr.Code, rr.Body.String())
	return rr.Body.String()
}

func TestProjectWorkspace_RendersAllTabs(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.CreateProject(ctx, db, core.Project{
		ID: mustID(t), Slug: "seamless", Name: "Seamless", Description: "memory substrate",
		CreatedAt: now, UpdatedAt: now,
	}))
	// Session IDs are ULIDs in production (the relation guards require it).
	sessID := mustID(t)
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: sessID, Name: "cc/s1", ProjectSlug: "seamless", Status: core.SessionActive,
		Findings: "building the tabbed workspace", CreatedAt: now, UpdatedAt: now,
	}))
	// A plan step the live session claims -> exercises the plan timeline, the
	// Sessions "holds" indicator, and the relations tree's claim edge.
	taskID := mustID(t)
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: taskID, ProjectSlug: "seamless", Title: "Build project detail",
		Status: core.TaskOpen, PlanSlug: "demo", CreatedAt: now, UpdatedAt: now,
	}))
	_, err := store.ClaimTask(ctx, db, taskID, sessID, 15*time.Minute, now)
	require.NoError(t, err)
	// A memory the session produced -> exercises the tree's memory leaf, the
	// Memories tab lineage cell, and memories-by-kind.
	insertRawMemory(t, db, mustID(t), "seamless", "cc/s1", now)

	body := getHTMLBody(t, mux, "/console/projects/seamless")
	for _, panel := range projectTabKeys {
		require.Contains(t, body, `data-panel="`+panel+`"`, "panel %q must render", panel)
	}
	require.Contains(t, body, "plan:demo", "plan slug on the tasks + relations panels")
	require.Contains(t, body, "cc/s1", "the claiming session appears on sessions + tree")
	require.Contains(t, body, "Build project detail", "the plan step title")
	require.Contains(t, body, "root project", "the shared-briefing banner (no parent/children)")
	require.Contains(t, body, "holds demo step", "the session's inline claim indicator")
}

func TestProjectWorkspace_DeepLinkTabAndFamilyChild(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, slug := range []string{"acme", "acme-ios"} {
		require.NoError(t, store.CreateProject(ctx, db, core.Project{
			ID: mustID(t), Slug: slug, Name: slug, CreatedAt: now, UpdatedAt: now,
		}))
	}
	require.NoError(t, store.SetProjectParent(ctx, db, "acme-ios", "acme", now))

	// A ?tab= deep-link marks that panel active server-side (no-JS + shareable).
	body := getHTMLBody(t, mux, "/console/projects/acme-ios?tab=relations")
	require.Contains(t, body, `class="tabpanel on" data-panel="relations"`)
	require.Contains(t, body, "child of acme", "the parent chip for a family child")
	require.Contains(t, body, "inherits", "the child inherits-from-parent banner")
}

func TestProjectWorkspace_BadTabIs400(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, store.CreateProject(ctx, db, core.Project{
		ID: mustID(t), Slug: "seamless", Name: "seamless", CreatedAt: now, UpdatedAt: now,
	}))

	req := httptest.NewRequest(http.MethodGet, "/console/projects/seamless?tab=bogus", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "overview, tasks, sessions", "a bad tab must name the valid values")
}

func TestProjectWorkspace_UnknownSlug404(t *testing.T) {
	_, mux := newConsole(t)
	req := httptest.NewRequest(http.MethodGet, "/console/projects/nope", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestProjectDetail_PeekIsThinSummary(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, store.CreateProject(ctx, db, core.Project{
		ID: mustID(t), Slug: "seamless", Name: "Seamless", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: "s1", Name: "cc/s1", ProjectSlug: "seamless", Status: core.SessionActive,
		CreatedAt: now, UpdatedAt: now,
	}))

	// The peek/CLI path returns the thin summary struct, not the workspace page.
	var d projectDetail
	getJSON(t, mux, "/console/projects/seamless?format=json", &d)
	require.Equal(t, "seamless", d.Slug)
	require.Equal(t, 1, d.Sessions)
}
