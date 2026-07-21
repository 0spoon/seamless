package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/store"
)

// findGroup returns the board group whose label starts with prefix.
func findGroup(t *testing.T, groups []projectGroupVM, prefix string) projectGroupVM {
	t.Helper()
	for _, g := range groups {
		if strings.HasPrefix(g.Label, prefix) {
			return g
		}
	}
	t.Fatalf("no group with label prefix %q in %v", prefix, groups)
	return projectGroupVM{}
}

func TestProjectsBoard_FamilyGrouping(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mkProj := func(slug string) {
		id, err := core.NewID()
		require.NoError(t, err)
		require.NoError(t, store.CreateProject(ctx, db, core.Project{
			ID: id, Slug: slug, Name: slug, CreatedAt: now, UpdatedAt: now,
		}))
	}
	mkSess := func(id, name, project string, status core.SessionStatus, updated time.Time) {
		require.NoError(t, store.CreateSession(ctx, db, core.Session{
			ID: id, Name: name, ProjectSlug: project, Status: status,
			CreatedAt: now, UpdatedAt: updated,
		}))
	}

	// A shared-briefing family (acme -> ios, web), a standalone, and a global
	// session so the "" scope surfaces as a Root row.
	for _, slug := range []string{"acme", "acme-ios", "acme-web", "solo", "old-proj"} {
		mkProj(slug)
	}
	require.NoError(t, store.SetProjectParent(ctx, db, "acme-ios", "acme", now))
	require.NoError(t, store.SetProjectParent(ctx, db, "acme-web", "acme", now))
	require.NoError(t, store.RetireProject(ctx, db, "old-proj", now))

	// One live session in a child (drives Working / LiveSessions), one global.
	mkSess("s1", "cc/s1", "acme-ios", core.SessionActive, now)
	mkSess("s2", "cc/s2", "", core.SessionCompleted, now)

	var data projectsData
	getJSON(t, mux, "/console/projects?format=json", &data)

	require.Equal(t, "family", data.Group)
	require.Equal(t, "recent", data.Sort)
	require.Equal(t, 5, data.Projects, "acme, acme-ios, acme-web, solo, old-proj (global excluded)")
	require.Equal(t, 1, data.Working)
	require.Equal(t, 1, data.Families)

	root := findGroup(t, data.Groups, "Root")
	require.Len(t, root.Rows, 1)
	require.True(t, root.Rows[0].Global)
	require.Equal(t, "global", root.Rows[0].Name)

	fam := findGroup(t, data.Groups, "acme · shared briefing")
	require.Equal(t, 3, fam.Count, "parent + 2 children")
	require.Equal(t, "acme", fam.Rows[0].Slug)
	require.False(t, fam.Rows[0].Child)
	require.True(t, fam.Rows[1].Child)
	require.True(t, fam.Rows[2].Child)
	require.Contains(t, fam.Note, "2 children")

	solo := findGroup(t, data.Groups, "Standalone")
	require.Len(t, solo.Rows, 1)
	require.Equal(t, "solo", solo.Rows[0].Slug)

	ret := findGroup(t, data.Groups, "Retired")
	require.Len(t, ret.Rows, 1)
	require.Equal(t, "old-proj", ret.Rows[0].Slug)
	require.True(t, ret.Rows[0].Retired)
}

func TestProjectsBoard_FlatMode(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, slug := range []string{"a", "b"} {
		id, err := core.NewID()
		require.NoError(t, err)
		require.NoError(t, store.CreateProject(ctx, db, core.Project{
			ID: id, Slug: slug, Name: slug, CreatedAt: now, UpdatedAt: now,
		}))
	}
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: "s1", Name: "cc/s1", ProjectSlug: "a", Status: core.SessionActive,
		CreatedAt: now, UpdatedAt: now,
	}))

	var data projectsData
	getJSON(t, mux, "/console/projects?format=json&group=flat&sort=name", &data)
	require.Equal(t, "flat", data.Group)
	require.Nil(t, data.Groups)
	require.Len(t, data.Flat, 2)
	require.Equal(t, "a", data.Flat[0].Slug) // sort=name
	require.Equal(t, "b", data.Flat[1].Slug)
}

func TestProjectsBoard_PortfolioSignalsAndRichCards(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.CreateProject(ctx, db, core.Project{
		ID: mustID(t), Slug: "seamless", Name: "Seamless", Description: "shared agent context",
		CreatedAt: now, UpdatedAt: now,
	}))
	sessionID := mustID(t)
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: sessionID, Name: "cc/projects", ProjectSlug: "seamless", Status: core.SessionActive,
		CreatedAt: now, UpdatedAt: now,
	}))
	_, err := db.ExecContext(ctx, `UPDATE sessions SET total_tokens = 12400 WHERE id = ?`, sessionID)
	require.NoError(t, err)
	memoryA, memoryB := mustID(t), mustID(t)
	insertMemoryRow(t, db, memoryA, "project-atlas", "seamless")
	insertMemoryRow(t, db, memoryB, "project-health", "seamless")

	recorder := events.NewRecorder(db)
	_, err = recorder.Record(ctx, core.Event{
		Kind: core.EventInjected, SessionID: sessionID, ProjectSlug: "seamless",
		Payload: map[string]any{"item_ids": []any{memoryA}},
	})
	require.NoError(t, err)

	blockerID := mustID(t)
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: blockerID, ProjectSlug: "seamless", Title: "decide the layout", Status: core.TaskOpen,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: mustID(t), ProjectSlug: "seamless", Title: "ship the layout", Status: core.TaskOpen,
		DependsOn: []string{blockerID}, CreatedAt: now, UpdatedAt: now,
	}))
	stamp := core.FormatTime(now)
	_, err = db.ExecContext(ctx, `
		INSERT INTO notes_index
		    (id, title, slug, description, project, file_path, tags, content_hash, created_at, updated_at)
		VALUES (?, 'Project brief', 'project-brief', '', 'seamless', 'notes/seamless/project-brief.md', '[]', 'hash', ?, ?)`,
		mustID(t), stamp, stamp)
	require.NoError(t, err)

	var data projectsData
	getJSON(t, mux, "/console/projects?format=json&group=flat&w=all", &data)
	require.Equal(t, 1, data.Projects)
	require.Equal(t, 1, data.Working)
	require.Equal(t, 1, data.LiveScopes)
	require.Equal(t, 2, data.Memories)
	require.Equal(t, 1, data.Notes)
	require.Equal(t, 12400, data.TokensTotal)
	require.Equal(t, 2, data.OpenTasks)
	require.Equal(t, 1, data.Blocked)
	require.Equal(t, 1, data.Attention)
	require.Equal(t, 1, data.Surfaced)
	require.Equal(t, 2, data.Active)
	require.Equal(t, 50, data.ReachRate)
	require.Len(t, data.Flat, 1)
	require.Equal(t, 1, data.Flat[0].Notes)
	require.Equal(t, 1, data.Flat[0].Surfaced)
	require.Equal(t, 2, data.Flat[0].Active)
	require.Equal(t, 12400, data.Flat[0].TokensTotal)

	body := getHTMLBody(t, mux, "/console/projects?w=all")
	require.Contains(t, body, `class="project-summary-grid"`)
	require.Contains(t, body, `class="project-reach-ring" style="--ring-val:50"`)
	require.Contains(t, body, `class="project-card row-link working blocked"`)
	require.Contains(t, body, "1 / 2 surfaced")
	require.Contains(t, body, "1 blocked")
	require.Contains(t, body, "12.4k tokens")
	require.Contains(t, body, "Model tokens")
	require.Contains(t, body, "Open workspace")
}

func TestProjectsBoard_BadParamsAre400(t *testing.T) {
	_, mux := newConsole(t)
	cases := []struct{ path, wants string }{
		{"/console/projects?group=bogus", "family, flat"},
		{"/console/projects?sort=bogus", "recent, coverage, name"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req.Header.Set("Authorization", "Bearer "+testKey)
		req.Header.Set("Accept", "application/json")
		rr := do(mux, req)
		require.Equal(t, http.StatusBadRequest, rr.Code, "GET %s", c.path)
		require.Contains(t, rr.Body.String(), c.wants, "GET %s must name the valid values", c.path)
	}
}
