package mcp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// favorite_set round-trips over every kind, the flag surfaces in the read
// tools, and the failure modes name their cause.
func TestFavoriteSet_AllKinds(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	// Bind a session in the mapped repo so unscoped writes land in "demo".
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "fav-sess"})

	// Seed one item per kind through the normal tool surface.
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "starrable", "kind": "gotcha", "description": "d", "body": "the body survives starring",
	})
	note := callJSON(t, ctx, cli, "notes_create", map[string]any{"title": "Starrable note", "body": "b"})
	noteID := note["id"].(string)
	task := callJSON(t, ctx, cli, "tasks_add", map[string]any{"title": "starrable task"})
	taskID := task["id"].(string)
	callJSON(t, ctx, cli, "lab_open", map[string]any{"lab": "fav-lab", "goal": "g"})
	trial := callJSON(t, ctx, cli, "trial_record", map[string]any{
		"title": "t", "changes": "c", "expected": "e", "actual": "a", "outcome": "pass",
	})
	trialID := trial["id"].(string)
	// A composed plan: a note tagged plan:<slug> is its primary.
	callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Plan narrative", "body": "b", "tags": []any{"plan:starrable-plan"},
	})

	set := func(kind, id string) map[string]any {
		return callJSON(t, ctx, cli, "favorite_set", map[string]any{"kind": kind, "id": id, "favorite": true})
	}
	for kind, id := range map[string]string{
		"memory": "starrable", "note": noteID, "project": "demo",
		"plan": "starrable-plan", "task": taskID, "session": "fav-sess", "trial": trialID,
	} {
		out := set(kind, id)
		require.Equal(t, true, out["favorite"], "kind %s", kind)
	}

	// The flag surfaces on reads, and the body survived the file rewrite.
	mem := callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "starrable"})
	require.Equal(t, true, mem["favorite"])
	require.Equal(t, "the body survives starring", strings.TrimSpace(mem["body"].(string)))
	nread := callJSON(t, ctx, cli, "notes_read", map[string]any{"id": noteID})
	require.Equal(t, true, nread["favorite"])
	tl := callJSON(t, ctx, cli, "tasks_list", map[string]any{"id": taskID})
	tasks := tl["tasks"].([]any)
	require.Equal(t, true, tasks[0].(map[string]any)["favorite"])

	// Unstar drops the key from reads entirely (omitted-when-false contract).
	out := callJSON(t, ctx, cli, "favorite_set", map[string]any{"kind": "memory", "id": "starrable", "favorite": false})
	require.Equal(t, false, out["favorite"])
	mem = callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "starrable"})
	require.NotContains(t, mem, "favorite")

	// Failure modes.
	isErr, msg := callErr(t, ctx, cli, "favorite_set",
		map[string]any{"kind": "memory", "id": "no-such-memory", "favorite": true})
	require.True(t, isErr)
	require.Contains(t, msg, "no memory")
	callJSON(t, ctx, cli, "tasks_add", map[string]any{"title": "step", "plan": "task-only-plan"})
	isErr, msg = callErr(t, ctx, cli, "favorite_set",
		map[string]any{"kind": "plan", "id": "task-only-plan", "favorite": true})
	require.True(t, isErr)
	require.Contains(t, msg, "has no note to favorite")
	require.Contains(t, msg, "task-only-plan")
}
