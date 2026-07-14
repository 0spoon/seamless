package mcp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// TestProjectArgRejectsUnsafeSlugs is the regression test for the project-slug
// traversal hole: MemoryRelPath/NoteRelPath join the project into the file path,
// and PathWithinDir only guards the data-dir boundary, so an unvalidated slug
// like "../notes/inbox" cleaned to a path INSIDE the data dir but outside its
// tree -- letting a memory_write clobber a note file (and vice versa). Every
// tool that accepts a project (or project slug) argument must reject unsafe
// values before any resolution or write.
func TestProjectArgRejectsUnsafeSlugs(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	// notes_update loads the note before validating fields, so it needs a real id.
	seeded := callJSON(t, ctx, cli, "notes_create", map[string]any{"title": "seed", "body": "b"})
	noteID, _ := seeded["id"].(string)
	require.NotEmpty(t, noteID)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"memory_write", map[string]any{
			"name": "x", "kind": "reference", "description": "d", "body": "b",
			"project": "../notes/inbox",
		}},
		{"notes_create", map[string]any{
			"title": "t", "body": "b", "project": "../memory/_global",
		}},
		{"tasks_add", map[string]any{"title": "t", "project": "a/b"}},
		{"memory_read", map[string]any{"name": "x", "project": ".."}},
		{"memory_append", map[string]any{"name": "x", "body": "b", "project": "..\\up"}},
		{"capture_url", map[string]any{"url": "https://example.com/", "project": "../x"}},
		{"trial_record", map[string]any{"title": "t", "lab": "L", "project": "../x"}},
		{"tasks_update", map[string]any{"id": "01JZZZZZZZZZZZZZZZZZZZZZZZ", "project": "../x"}},
		{"notes_update", map[string]any{"id": noteID, "project": "../x"}},
	} {
		isErr, txt := callErr(t, ctx, cli, tc.tool, tc.args)
		require.True(t, isErr, "%s must reject an unsafe project slug", tc.tool)
		require.Contains(t, txt, "invalid project", "%s: %s", tc.tool, txt)
	}

	// project_create guards the same invariant at the slug's birth.
	isErr, txt := callErr(t, ctx, cli, "project_create", map[string]any{"name": "Evil", "slug": "../evil"})
	require.True(t, isErr, "project_create must reject an unsafe slug")
	require.Contains(t, txt, "invalid slug", txt)
}

// TestTasksClaimRejectsOverflowLease pins the lease_seconds overflow guard: a
// value whose seconds->Duration conversion overflows int64 used to produce a
// negative lease, so the claim reported success while being instantly expired
// (silently reclaimable by anyone).
func TestTasksClaimRejectsOverflowLease(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	task := callJSON(t, ctx, cli, "tasks_add", map[string]any{"title": "claim me"})
	id, _ := task["id"].(string)
	require.NotEmpty(t, id)

	isErr, txt := callErr(t, ctx, cli, "tasks_claim", map[string]any{
		"id": id, "lease_seconds": "99999999999999",
	})
	require.True(t, isErr, "an overflowing lease_seconds must be rejected")
	require.Contains(t, txt, "invalid lease_seconds")

	// A sane lease still claims, and the lease expiry is in the future.
	claimed := callJSON(t, ctx, cli, "tasks_claim", map[string]any{"id": id, "lease_seconds": "60"})
	require.Equal(t, "in_progress", claimed["status"])
	require.NotEmpty(t, claimed["lease_expires_at"])
}

// TestTasksUpdateRejectsBlankTitle pins the blank-title guard: tasks_add
// requires a title, so tasks_update must not be able to blank one.
func TestTasksUpdateRejectsBlankTitle(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	task := callJSON(t, ctx, cli, "tasks_add", map[string]any{"title": "keep my title"})
	id, _ := task["id"].(string)

	isErr, txt := callErr(t, ctx, cli, "tasks_update", map[string]any{"id": id, "title": "   "})
	require.True(t, isErr, "a blank title must be rejected")
	require.Contains(t, txt, "title must not be blank")

	got := callJSON(t, ctx, cli, "tasks_list", map[string]any{"id": id})
	require.Equal(t, "keep my title", got["tasks"].([]any)[0].(map[string]any)["title"])
}

// TestNotesUpdateProjectMove covers the note move recipe: a successful move
// lands the note under the new project and drops the old path; a move onto a
// path another note already owns is refused BEFORE any file is touched, so
// neither note is damaged (the old ordering removed the source file first, so a
// failed write deleted the note outright).
func TestNotesUpdateProjectMove(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	// A clean move: demo -> global inbox.
	moved := callJSON(t, ctx, cli, "notes_create", map[string]any{"title": "Move Me", "body": "content survives"})
	movedID, _ := moved["id"].(string)
	callJSON(t, ctx, cli, "notes_update", map[string]any{"id": movedID, "project": "global"})

	_, stillInDemo, err := store.NoteBySlug(ctx, db, "demo", "move-me")
	require.NoError(t, err)
	require.False(t, stillInDemo, "the old project must no longer index the note")
	inInbox, found, err := store.NoteBySlug(ctx, db, "", "move-me")
	require.NoError(t, err)
	require.True(t, found, "the note lands in the global inbox")
	require.Equal(t, movedID, inInbox.ID)
	r := callJSON(t, ctx, cli, "notes_read", map[string]any{"id": movedID})
	require.Contains(t, r["body"], "content survives")

	// A conflicting move: the target path is owned by a different note.
	a := callJSON(t, ctx, cli, "notes_create", map[string]any{"title": "Same Slug", "body": "note A"})
	aID, _ := a["id"].(string)
	callJSON(t, ctx, cli, "notes_create", map[string]any{"title": "Same Slug", "body": "note B", "project": "global"})

	isErr, txt := callErr(t, ctx, cli, "notes_update", map[string]any{"id": aID, "project": "global"})
	require.True(t, isErr, "a move onto an occupied path must be refused")
	require.Contains(t, txt, "already exists")

	// Both notes are intact: A still in demo with its body, B untouched in inbox.
	ra := callJSON(t, ctx, cli, "notes_read", map[string]any{"id": aID})
	require.Contains(t, ra["body"], "note A")
	require.Equal(t, "demo", ra["project"])
	b, found, err := store.NoteBySlug(ctx, db, "", "same-slug")
	require.NoError(t, err)
	require.True(t, found)
	rb := callJSON(t, ctx, cli, "notes_read", map[string]any{"id": b.ID})
	require.Contains(t, rb["body"], "note B")
}

// TestNotesCreateRejectsInvalidTitle enforces the validate.Title invariant at
// the MCP boundary (AGENTS.md: human titles go through validate.Title).
func TestNotesCreateRejectsInvalidTitle(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	isErr, txt := callErr(t, ctx, cli, "notes_create", map[string]any{
		"title": strings.Repeat("x", 300), "body": "b", "project": "global",
	})
	require.True(t, isErr, "an over-long title must be rejected")
	require.Contains(t, txt, "255")
}

// TestSessionStartResumeReactivates pins the resume semantic: reusing a name
// after session_end must flip the session back to active (and bump recency), or
// the per-call heartbeat -- which only touches active sessions -- silently
// no-ops and every surface treats the resumed agent as gone.
func TestSessionStartResumeReactivates(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)

	cli := dialClient(t, ctx, url, testKey)
	start := callJSON(t, ctx, cli, "session_start", map[string]any{
		"name": "agent-resume", "cwd": "/work/demo", "source": "startup",
	})
	id, _ := start["session_id"].(string)
	callJSON(t, ctx, cli, "session_end", map[string]any{"findings": "first stint done"})

	sess, ok, err := store.SessionByID(ctx, db, id)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, core.SessionCompleted, sess.Status)

	cli2 := dialClient(t, ctx, url, testKey)
	start2 := callJSON(t, ctx, cli2, "session_start", map[string]any{
		"name": "agent-resume", "cwd": "/work/demo", "source": "resume",
	})
	require.Equal(t, id, start2["session_id"], "the named resume targets the same session")
	require.Equal(t, true, start2["resumed"])

	sess, ok, err = store.SessionByID(ctx, db, id)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, core.SessionActive, sess.Status, "a resumed session must be active again")
}
