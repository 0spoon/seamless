package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

func TestTasksPage_ReadyBlockedClosed(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mk := func(title string, deps ...string) core.Task {
		id, err := core.NewID()
		require.NoError(t, err)
		task := core.Task{
			ID: id, ProjectSlug: "seamless", Title: title, Status: core.TaskOpen,
			DependsOn: deps, CreatedAt: now, UpdatedAt: now,
		}
		require.NoError(t, store.CreateTask(ctx, db, task))
		return task
	}

	a := mk("build the widget") // ready
	mk("ship the widget", a.ID) // blocked by a
	c := mk("write docs")       // will be marked done
	_, err := store.UpdateTask(ctx, db, c.ID, store.TaskPatch{Status: ptrStatus(core.TaskDone)}, "", now)
	require.NoError(t, err)

	var data tasksData
	getJSON(t, mux, "/console/tasks?format=json", &data)

	require.Len(t, data.Ready, 1)
	require.Equal(t, "build the widget", data.Ready[0].Title)
	require.Len(t, data.Blocked, 1)
	require.Equal(t, "ship the widget", data.Blocked[0].Title)
	require.Len(t, data.Blocked[0].Blockers, 1)
	require.Equal(t, "build the widget", data.Blocked[0].Blockers[0].Title)
	require.Len(t, data.Closed, 1)
	require.Equal(t, "write docs", data.Closed[0].Title)
	require.Equal(t, "done", data.Closed[0].Status)

	// HTML renders.
	req := httptest.NewRequest(http.MethodGet, "/console/tasks", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "ship the widget")
}

func ptrStatus(s core.TaskStatus) *core.TaskStatus { return &s }

// TestTaskRelease_OwnerOverride covers the console release-lock button: the peek
// surfaces the holder and offers the button, and POSTing the release reopens the
// task (clearing the claim) regardless of who held it.
func TestTaskRelease_OwnerOverride(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	id, err := core.NewID()
	require.NoError(t, err)
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: id, ProjectSlug: "seamless", Title: "held task", Status: core.TaskOpen,
		CreatedAt: now, UpdatedAt: now,
	}))
	_, err = store.ClaimTask(ctx, db, id, "cc/agent-x", time.Minute, now)
	require.NoError(t, err)

	// The peek surfaces the holder and the release-lock button.
	req := httptest.NewRequest(http.MethodGet, "/console/tasks/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "cc/agent-x")
	require.Contains(t, rr.Body.String(), "release lock")

	// POST the release: a browser gets a redirect back to the task.
	rr = do(mux, postAuthed("/console/tasks/"+id+"/release"))
	require.Equal(t, http.StatusSeeOther, rr.Code)

	// The claim is cleared and the task is open again.
	got, err := store.TaskByID(ctx, db, id)
	require.NoError(t, err)
	require.Equal(t, core.TaskOpen, got.Status)
	require.Empty(t, got.ClaimedBy)
	require.Nil(t, got.LeaseExpiresAt)

	// The button is gone now that nothing holds the task.
	req = httptest.NewRequest(http.MethodGet, "/console/tasks/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr = do(mux, req)
	require.NotContains(t, rr.Body.String(), "release lock")

	// A JSON caller (the seam CLI --force path) gets 200 with the reopened task;
	// releasing an unclaimed task is an error, not a silent success.
	_, err = store.ClaimTask(ctx, db, id, "cc/agent-y", time.Minute, time.Now().UTC())
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPost, "/console/tasks/"+id+"/release", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("Accept", "application/json")
	rr = do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"status": "open"`)
}

// postAuthed builds an authenticated POST request (no body) for a console action.
func postAuthed(path string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	return req
}
