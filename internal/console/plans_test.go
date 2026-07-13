package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/store"
)

// seedPlanComposition writes a presented cc-plan note (iteration 3) plus one
// attached agent-cache note under plan:my-plan in project demo.
func seedPlanComposition(t *testing.T, mgr *files.Manager) core.Note {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	planID, err := core.NewID()
	require.NoError(t, err)
	planNote, err := mgr.WriteNote(ctx, core.Note{
		ID: planID, Slug: "cc-plan-clever-stallman", Title: "My Plan", Project: "demo",
		Description: plans.NoteDescription("clever-stallman", 3, plans.StatusPresented),
		Body:        "> captured from cc/abcdef12 | clever-stallman.md | iter 3 | git feedface | " + now.Format(time.RFC3339) + "\n\n# My Plan\n\nDo the thing.",
		Tags:        []string{"plan:my-plan", plans.TagPlan, "plan-status:presented", "created-by:agent"},
		Extra:       map[string]any{"plan_iteration": 3},
		Created:     now, Updated: now,
	})
	require.NoError(t, err)

	agentID, err := core.NewID()
	require.NoError(t, err)
	_, err = mgr.WriteNote(ctx, core.Note{
		ID: agentID, Slug: "cc-agent-abc123", Title: "[Explore] Explore the gardener", Project: "demo",
		Description: "Cached planning-subagent run (Explore) -- prompt + final report",
		Body:        "> captured from cc/abcdef12 | agent abc123 | git feedface | " + now.Format(time.RFC3339) + "\n\n## Prompt\n\nExplore\n\n## Report\n\nDone.",
		Tags:        []string{"plan:my-plan", plans.TagAgent, "agent:Explore", "created-by:agent"},
		Created:     now, Updated: now,
	})
	require.NoError(t, err)
	return planNote
}

func TestPlans_ListAndDetail(t *testing.T) {
	_, mgr, mux := newConsoleWithFiles(t)
	planNote := seedPlanComposition(t, mgr)

	var list plansData
	getJSON(t, mux, "/console/plans?format=json&w=all", &list)
	require.Equal(t, 1, list.Count)
	row := list.Rows[0]
	require.Equal(t, "my-plan", row.Slug)
	require.Equal(t, "clever-stallman", row.Basename)
	require.Equal(t, "My Plan", row.Title)
	require.Equal(t, "demo", row.Project)
	require.Equal(t, plans.StatusPresented, row.Status)
	require.Equal(t, 3, row.Iteration)
	require.Equal(t, 1, row.Agents)
	require.Equal(t, planNote.ID, row.NoteID)

	var d planDetailData
	getJSON(t, mux, "/console/plans/my-plan?format=json", &d)
	require.Equal(t, "my-plan", d.Row.Slug)
	require.True(t, d.BodyLoaded)
	require.Contains(t, d.BodyText, "Do the thing.")
	require.True(t, d.CanApprove)
	require.Len(t, d.Attached, 1)
	require.True(t, d.Attached[0].IsAgent)
	require.Equal(t, "cc-agent-abc123", d.Attached[0].Slug)

	// Unknown slug -> 404.
	req := httptest.NewRequest(http.MethodGet, "/console/plans/no-such-plan?format=json", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	require.Equal(t, http.StatusNotFound, do(mux, req).Code)
}

func TestPlans_ApproveEscapeHatch(t *testing.T) {
	db, mgr, mux := newConsoleWithFiles(t)
	seedPlanComposition(t, mgr)
	ctx := context.Background()
	rec := events.NewRecorder(db)

	approve := func() map[string]any {
		req := httptest.NewRequest(http.MethodPost, "/console/plans/my-plan/approve?format=json", nil)
		req.Header.Set("Authorization", "Bearer "+testKey)
		req.Header.Set("Accept", "application/json")
		rr := do(mux, req)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
		return out
	}

	out := approve()
	require.Equal(t, "approved", out["status"])
	require.Equal(t, true, out["taskCreated"])

	// The note flipped and the tracking task exists.
	idx, ok, err := store.NoteBySlug(ctx, db, "demo", "cc-plan-clever-stallman")
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, idx.Tags, "plan-status:approved")
	tasks, err := store.ListTasksForPlan(ctx, db, "demo", "", "my-plan")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "Implement plan: My Plan", tasks[0].Title)
	require.Equal(t, "console", tasks[0].CreatedBy)

	evs, err := rec.ByKinds(ctx, []core.EventKind{core.EventPlanApproved}, "", "", 10)
	require.NoError(t, err)
	require.Len(t, evs, 1)
	require.Equal(t, "console", evs[0].Payload["by"])

	// Re-approval is idempotent: no second task, no second approved event.
	out = approve()
	require.Equal(t, false, out["taskCreated"])
	tasks, err = store.ListTasksForPlan(ctx, db, "demo", "", "my-plan")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	evs, err = rec.ByKinds(ctx, []core.EventKind{core.EventPlanApproved}, "", "", 10)
	require.NoError(t, err)
	require.Len(t, evs, 1)

	// Detail no longer offers the approve action.
	var d planDetailData
	getJSON(t, mux, "/console/plans/my-plan?format=json", &d)
	require.False(t, d.CanApprove)
}

func TestPlans_HTMLShellRenders(t *testing.T) {
	_, mgr, mux := newConsoleWithFiles(t)
	seedPlanComposition(t, mgr)

	req := httptest.NewRequest(http.MethodGet, "/console/plans?w=all", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "My Plan")
	require.Contains(t, rr.Body.String(), "plan:my-plan")

	req = httptest.NewRequest(http.MethodGet, "/console/plans/my-plan", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr = do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "Do the thing.")
	require.Contains(t, rr.Body.String(), "agent-cache")
}
