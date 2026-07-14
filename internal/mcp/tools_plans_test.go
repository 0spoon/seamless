package mcp_test

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// TestPlanComposition exercises the plans-as-composition surface end to end:
// plan-tagged tasks stay out of the default queue, tasks_claim is an atomic
// claim, tasks_release reopens, and the briefing shows the plan rollup.
func TestPlanComposition(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	// A plain task and a plan step.
	plain := callJSON(t, ctx, cli, "tasks_add", map[string]any{"title": "plain task"})
	step := callJSON(t, ctx, cli, "tasks_add", map[string]any{"title": "plan step", "plan": "demo-plan"})
	stepID := step["id"].(string)
	require.Equal(t, "demo-plan", step["plan"])

	// The default ready-queue excludes the plan step.
	ready := callJSON(t, ctx, cli, "tasks_ready", nil)
	readyList := ready["ready"].([]any)
	require.Len(t, readyList, 1)
	require.Equal(t, plain["id"], readyList[0].(map[string]any)["id"])

	// The plan filter surfaces only the plan's steps.
	planReady := callJSON(t, ctx, cli, "tasks_ready", map[string]any{"plan": "demo-plan"})
	pr := planReady["ready"].([]any)
	require.Len(t, pr, 1)
	require.Equal(t, stepID, pr[0].(map[string]any)["id"])

	// Claim the step: it becomes in_progress with a claim + lease.
	claimed := callJSON(t, ctx, cli, "tasks_claim", map[string]any{"id": stepID})
	require.Equal(t, "in_progress", claimed["status"])
	require.NotEmpty(t, claimed["claimed_by"])
	require.NotEmpty(t, claimed["lease_expires_at"])

	// The briefing surfaces the active-plan rollup: 0/1 done, 0 claimable, 1 in
	// flight. Read it on a separate connection so cli's session binding (and thus
	// the claim holder) is untouched.
	briefCli := dialClient(t, ctx, url, testKey)
	brief := callJSON(t, ctx, briefCli, "session_start", map[string]any{"cwd": "/work/demo", "source": "resume"})
	require.Contains(t, brief["briefing"], "PLAN: demo-plan -- 0/1 done, 0 claimable, 1 in flight")

	// Release reopens the step so it is claimable again.
	released := callJSON(t, ctx, cli, "tasks_release", map[string]any{"id": stepID})
	require.Equal(t, "open", released["status"])
	require.Empty(t, released["claimed_by"])
}

// TestNotesCreatePlanTag confirms notes_create plan=<slug> carries the note into
// the plan:<slug> composition -- the same key tasks_add plan= writes -- so an
// agent attaches a plan's narrative without hand-typing the tag prefix.
func TestNotesCreatePlanTag(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	nc := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Refactor plan", "body": "The narrative.", "plan": "refactor-x",
	})
	require.Equal(t, "refactor-x", nc["plan"])

	nr := callJSON(t, ctx, cli, "notes_read", map[string]any{"id": nc["id"]})
	tags := nr["tags"].([]any)
	require.Contains(t, tags, "plan:refactor-x")

	// A note created without a plan carries no composition tag.
	plain := callJSON(t, ctx, cli, "notes_create", map[string]any{"title": "Loose note", "body": "b"})
	require.NotContains(t, plain, "plan")
}

// TestClaimConflictAcrossSessions confirms a second session cannot claim a task
// the first session already holds.
func TestClaimConflictAcrossSessions(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)

	// Session A claims the task.
	cliA := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cliA, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})
	task := callJSON(t, ctx, cliA, "tasks_add", map[string]any{"title": "contended"})
	id := task["id"].(string)
	callJSON(t, ctx, cliA, "tasks_claim", map[string]any{"id": id})

	// Session B (a different connection/session in the same project) is refused.
	cliB := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cliB, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup", "name": "agent-b"})
	res, err := cliB.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "tasks_claim", Arguments: map[string]any{"id": id},
	}})
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "already claimed")
}

// TestUpdateRejectedForNonHolder confirms the write-lock over MCP: a session
// that does not hold a task's live claim cannot mutate it via tasks_update, but
// the holder can, and a released task is updatable by anyone again.
func TestUpdateRejectedForNonHolder(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)

	// Session A claims the task.
	cliA := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cliA, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})
	task := callJSON(t, ctx, cliA, "tasks_add", map[string]any{"title": "held work"})
	id := task["id"].(string)
	callJSON(t, ctx, cliA, "tasks_claim", map[string]any{"id": id})

	// Session B cannot close it out from under the holder.
	cliB := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cliB, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup", "name": "agent-b"})
	res, err := cliB.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "tasks_update", Arguments: map[string]any{"id": id, "status": "done"},
	}})
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "already claimed")

	// The holder updates its own task normally.
	updated := callJSON(t, ctx, cliA, "tasks_update", map[string]any{"id": id, "body": "in progress"})
	require.Equal(t, "in progress", updated["body"])

	// Once A releases, B may update the (now open) task.
	callJSON(t, ctx, cliA, "tasks_release", map[string]any{"id": id})
	reopened := callJSON(t, ctx, cliB, "tasks_update", map[string]any{"id": id, "status": "done"})
	require.Equal(t, "done", reopened["status"])
}

// TestSessionEndReleasesClaims confirms ending a session returns its in-flight
// claims to the queue.
func TestSessionEndReleasesClaims(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	task := callJSON(t, ctx, cli, "tasks_add", map[string]any{"title": "claim then leave"})
	id := task["id"].(string)
	callJSON(t, ctx, cli, "tasks_claim", map[string]any{"id": id})

	end := callJSON(t, ctx, cli, "session_end", map[string]any{"findings": "done for now"})
	require.EqualValues(t, 1, end["claims_released"])

	// The task is open again (a fresh session sees it ready).
	cli2 := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli2, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})
	ready := callJSON(t, ctx, cli2, "tasks_ready", nil)
	require.Len(t, ready["ready"].([]any), 1)
}
