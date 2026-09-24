package mcp_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// noteEventRows reads one event kind straight off the append-only log, oldest
// first, with the columns the note events are judged on folded into the payload
// map under keys no payload can collide with.
func noteEventRows(t *testing.T, db *sql.DB, kind core.EventKind) []map[string]any {
	t.Helper()
	rows, err := db.Query(`SELECT project_slug, session_id, item_id, payload FROM events WHERE kind = ? ORDER BY id`,
		string(kind))
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []map[string]any
	for rows.Next() {
		var project, session, item, payload string
		require.NoError(t, rows.Scan(&project, &session, &item, &payload))
		p := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(payload), &p))
		p["_project"], p["_session"], p["_item"] = project, session, item
		out = append(out, p)
	}
	require.NoError(t, rows.Err())
	return out
}

// Every note mutation is a durable domain event. notes_update, notes_append and
// notes_delete recorded nothing at all, so the console feed, the activity
// counts, and any later reconstruction of what an agent did saw notes as
// write-once artifacts that never changed and never disappeared. Each event
// carries a small discriminating payload so the three are distinguishable
// without diffing files.
func TestNoteMutations_RecordEvents(t *testing.T) {
	ctx := context.Background()
	url, db, _, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)

	start := callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-events-sess"})
	sessID, _ := start["session_id"].(string)
	require.NotEmpty(t, sessID)

	created := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Event note", "body": "the original body",
	})
	noteID, _ := created["id"].(string)

	callJSON(t, ctx, cli, "notes_update", map[string]any{"id": noteID, "title": "Event note revised"})
	callJSON(t, ctx, cli, "notes_append", map[string]any{"id": noteID, "body": "one more data point"})

	written := noteEventRows(t, db, core.EventNoteWritten)
	require.Len(t, written, 3, "create, update and append are each a note.written")
	for i, ev := range written {
		require.Equal(t, noteID, ev["_item"], "event %d must name the note it changed", i)
		require.Equal(t, "demo", ev["_project"], "event %d", i)
		require.Equal(t, sessID, ev["_session"], "event %d must carry the writing session", i)
	}
	require.Equal(t, "Event note", written[0]["title"])
	require.Nil(t, written[0]["updated"], "the create is not an update")

	require.Equal(t, true, written[1]["updated"], "notes_update must be discriminable from a create")
	require.Equal(t, "Event note revised", written[1]["title"])

	require.Equal(t, true, written[2]["appended"], "notes_append must be discriminable from a replace")
	require.Nil(t, written[2]["updated"])

	// notes_delete lands in the project the note ACTUALLY sat in, not the caller's
	// scope: the delete is by globally-unique id, so a note created elsewhere is
	// reachable from here and an event filed under "demo" would tell the console
	// the wrong story about both projects.
	elsewhere := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Doomed note", "body": "b", "project": "other",
	})
	callJSON(t, ctx, cli, "notes_delete", map[string]any{"id": elsewhere["id"]})

	written = noteEventRows(t, db, core.EventNoteWritten)
	require.Len(t, written, 5, "the out-of-project create and its delete are both events")
	del := written[4]
	require.Equal(t, true, del["deleted"])
	require.Equal(t, elsewhere["id"], del["_item"])
	require.Equal(t, "other", del["_project"], "a delete is recorded against the project the note sat in")
}

// note.read is the note-side twin of memory.read: a QUERY-GATED demand signal
// that only exists if the read records one. It must fire for both addressing
// modes, and it must not fire for a read that never happened.
func TestNotesRead_RecordsReadEvent(t *testing.T) {
	ctx := context.Background()
	url, db, _, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)

	start := callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-read-sess"})
	sessID, _ := start["session_id"].(string)

	created := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Read me", "body": "the body",
	})
	noteID, _ := created["id"].(string)
	slug, _ := created["slug"].(string)

	require.Empty(t, noteEventRows(t, db, core.EventNoteRead), "creating a note is not reading it")

	callJSON(t, ctx, cli, "notes_read", map[string]any{"id": noteID})
	callJSON(t, ctx, cli, "notes_read", map[string]any{"slug": slug})

	reads := noteEventRows(t, db, core.EventNoteRead)
	require.Len(t, reads, 2, "both the by-id and the by-slug read are demand signals")
	for i, ev := range reads {
		require.Equal(t, noteID, ev["_item"], "read %d must name the note", i)
		require.Equal(t, "demo", ev["_project"], "read %d", i)
		require.Equal(t, sessID, ev["_session"], "read %d must carry the reading session", i)
		require.Equal(t, slug, ev["slug"], "read %d payload names the note by slug", i)
	}

	// A miss is not a read: nothing was returned, so nothing is demanded.
	isErr, _ := callErr(t, ctx, cli, "notes_read", map[string]any{"slug": "no-such-note"})
	require.True(t, isErr)
	require.Len(t, noteEventRows(t, db, core.EventNoteRead), 2, "a failed read must not record one")
}
