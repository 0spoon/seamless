package mcp_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// seedTask inserts an open, unclaimed task in project and returns its id, for the
// actor-resolution tests that need a task to claim/update without going through the
// (scope-guarded) tasks_add path on an unbound connection.
func seedTask(t *testing.T, ctx context.Context, db *sql.DB, project, title string) string {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: id, ProjectSlug: project, Title: title, Status: core.TaskOpen,
		CreatedAt: now, UpdatedAt: now,
	}))
	return id
}

// TestClaimActorSoloAmbientResolves is the core ambient-as-identity property: a
// solo agent that never called session_start (or whose transport binding was lost
// on reconnect) still owns its claim, because the actor is recovered from the sole
// active ambient on every call rather than from a fragile per-transport binding.
func TestClaimActorSoloAmbientResolves(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	amb := seedAmbient(t, ctx, db, "cc/soloamb0", "demo")
	id := seedTask(t, ctx, db, "demo", "solo work")

	// This connection never calls session_start -> no binding. The claim is still
	// attributed to the agent's ambient session.
	cli := dialClient(t, ctx, url, testKey)
	claimed := callJSON(t, ctx, cli, "tasks_claim", map[string]any{"id": id})
	require.Equal(t, "in_progress", claimed["status"])
	require.Equal(t, amb, claimed["claimed_by"], "a solo agent's claim is owned by its ambient, no binding needed")

	// Reconnect: a fresh transport (new Mcp-Session-Id, so no binding) recovers the
	// SAME identity from the sole ambient, so it can update and release the claim it
	// owns -- exactly what a transport-keyed binding lost across reconnect could not.
	cli2 := dialClient(t, ctx, url, testKey)
	upd := callJSON(t, ctx, cli2, "tasks_update", map[string]any{"id": id, "body": "after reconnect"})
	require.Equal(t, "after reconnect", upd["body"])
	rel := callJSON(t, ctx, cli2, "tasks_release", map[string]any{"id": id})
	require.Equal(t, "open", rel["status"])
	require.Empty(t, rel["claimed_by"])
}

// TestClaimActorAmbiguousRefusesThenNamed pins the anti-corruption behavior: with
// no binding and two agents active in the same project, a claim must refuse rather
// than guess an owner (the cross-agent claim-bleed bug). The agent disambiguates by
// naming itself with the cc/<id> its briefing prints.
func TestClaimActorAmbiguousRefusesThenNamed(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	a := seedAmbient(t, ctx, db, "cc/ambaaaa0", "demo")
	seedAmbient(t, ctx, db, "cc/ambbbbb0", "demo")
	id := seedTask(t, ctx, db, "demo", "contended work")

	cli := dialClient(t, ctx, url, testKey) // unbound

	isErr, txt := callErr(t, ctx, cli, "tasks_claim", map[string]any{"id": id})
	require.True(t, isErr, "an unbound claim under concurrent same-project agents must refuse")
	require.Contains(t, txt, "ambiguous agent", "the refusal must name the actor ambiguity: %s", txt)

	// Naming itself resolves the claim to exactly that session.
	claimed := callJSON(t, ctx, cli, "tasks_claim", map[string]any{"id": id, "session": "cc/ambaaaa0"})
	require.Equal(t, a, claimed["claimed_by"], "the named agent owns the claim")
}

// TestUpdateActorSentinelUnderAmbiguity verifies the update path degrades correctly
// under actor ambiguity: an unclaimed task stays freely editable (the holder-lock
// does not apply, so the sentinel actor is fine), but a task under a sibling's live
// claim is refused -- a concurrent agent cannot mutate a held task without naming
// itself as the holder.
func TestUpdateActorSentinelUnderAmbiguity(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	a := seedAmbient(t, ctx, db, "cc/ambaaaa0", "demo")
	seedAmbient(t, ctx, db, "cc/ambbbbb0", "demo")

	cli := dialClient(t, ctx, url, testKey) // unbound, ambiguous actor

	// An UNCLAIMED task edits despite the ambiguity: no holder-lock to satisfy.
	open := seedTask(t, ctx, db, "demo", "open task")
	upd := callJSON(t, ctx, cli, "tasks_update", map[string]any{"id": open, "body": "edited"})
	require.Equal(t, "edited", upd["body"], "an unclaimed task stays editable under an ambiguous actor")

	// A task held by agent A's live claim is NOT mutable by the ambiguous (sentinel)
	// actor: the holder-lock refuses it.
	held := seedTask(t, ctx, db, "demo", "held task")
	_, err := store.ClaimTask(ctx, db, held, a, time.Minute, time.Now().UTC())
	require.NoError(t, err)
	isErr, txt := callErr(t, ctx, cli, "tasks_update", map[string]any{"id": held, "status": "done"})
	require.True(t, isErr, "an ambiguous actor must not mutate a task held by a sibling")
	require.Contains(t, txt, "already claimed", "%s", txt)

	// Naming itself as the holder lets A close its own held task.
	done := callJSON(t, ctx, cli, "tasks_update", map[string]any{"id": held, "status": "done", "session": "cc/ambaaaa0"})
	require.Equal(t, "done", done["status"], "the named holder can close its own task")
}

// TestClaimActorBindingWinsOverAmbient verifies a bound agent (called session_start)
// claims as its own session even when a sibling ambient exists in the same project:
// the binding short-circuits the ambient ambiguity guard.
func TestClaimActorBindingWinsOverAmbient(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)

	cli := dialClient(t, ctx, url, testKey)
	start := callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})
	bound, _ := start["session_id"].(string)
	require.NotEmpty(t, bound)

	// A sibling ambient would make an unbound actor ambiguous; the binding wins.
	seedAmbient(t, ctx, db, "cc/sibling0", "demo")
	id := seedTask(t, ctx, db, "demo", "bound work")

	claimed := callJSON(t, ctx, cli, "tasks_claim", map[string]any{"id": id})
	require.Equal(t, bound, claimed["claimed_by"], "the bound session owns the claim, not the sibling ambient")
}
