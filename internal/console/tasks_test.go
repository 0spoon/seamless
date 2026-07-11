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
	_, err := store.UpdateTask(ctx, db, c.ID, store.TaskPatch{Status: ptrStatus(core.TaskDone)}, now)
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
