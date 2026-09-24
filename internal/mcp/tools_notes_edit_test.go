package mcp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/files"
)

// noteFileBytes reads a note's markdown file exactly as it sits on disk. The
// byte comparison is the point: "the refused edit changed nothing" is a claim
// about the file, and asserting on a parsed struct would miss frontmatter the
// parser normalizes away (an Updated stamp that moved, a re-ordered key).
func noteFileBytes(t *testing.T, dir, project, slug string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(files.NoteRelPath(project, slug)))) //nolint:gosec // test-owned path under t.TempDir()
	require.NoError(t, err)
	return raw
}

// noteEditArg builds one element of the edits[] argument. Deliberately its own
// helper rather than a shared one: the note and memory edit suites are separate
// files, and a same-package collision between two spellings of a two-line
// literal is a worse trade than the repetition.
func noteEditArg(oldStr, newStr string) map[string]any {
	return map[string]any{"old_string": oldStr, "new_string": newStr}
}

// A unique match changes exactly what it names and nothing around it, and the
// returned content_hash is the FILE's -- the handle the expect_hash precondition
// is checked against, so a hash describing anything else would make the
// precondition inert.
func TestNotesEdit_UniqueEditChangesOnlyItsTarget(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, dir := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-edit-unique"})

	created := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Boot race writeup",
		"body":  "The daemon binds 127.0.0.1:8080 by default.\nThe fix was a readiness gate.\nThe data dir is ~/.seamless.\n",
	})
	noteID, _ := created["id"].(string)
	slug, _ := created["slug"].(string)

	out := callJSON(t, ctx, cli, "notes_edit", map[string]any{
		"id":    noteID,
		"edits": []any{noteEditArg("127.0.0.1:8080", "127.0.0.1:8081")},
	})
	require.Equal(t, noteID, out["id"])
	require.Equal(t, "Boot race writeup", out["title"], "the response names the note it edited")

	onDisk, err := mgr.Store().ReadNote(files.NoteRelPath("demo", slug))
	require.NoError(t, err)
	require.Contains(t, onDisk.Body, "127.0.0.1:8081")
	require.NotContains(t, onDisk.Body, "8080", "the replaced text must be gone")
	require.Contains(t, onDisk.Body, "The fix was a readiness gate.", "untouched lines must survive verbatim")
	require.Contains(t, onDisk.Body, "The data dir is ~/.seamless.")

	require.Equal(t, files.ContentHash(string(noteFileBytes(t, dir, "demo", slug))), out["content_hash"],
		"content_hash must be the file's, so a follow-up expect_hash matches")

	diff, _ := out["diff"].(string)
	require.Contains(t, diff, "-The daemon binds 127.0.0.1:8080 by default.")
	require.Contains(t, diff, "+The daemon binds 127.0.0.1:8081 by default.")
}

// The unique-or-fail guard is the whole safety story at the tool surface: an
// ambiguous old_string is refused with the count (so the agent knows what to do
// about it), and the file is byte-identical afterwards.
func TestNotesEdit_AmbiguousMatchRefusesAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	url, _, _, dir := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-edit-ambiguous"})

	created := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Release checklist",
		"body":  "TODO wire the flag\nTODO write the test\nTODO update the docs\n",
	})
	slug, _ := created["slug"].(string)
	before := noteFileBytes(t, dir, "demo", slug)

	isErr, text := callErr(t, ctx, cli, "notes_edit", map[string]any{
		"id":    created["id"],
		"edits": []any{noteEditArg("TODO", "DONE")},
	})
	require.True(t, isErr, "an ambiguous old_string must be refused: %s", text)
	require.Contains(t, text, "matches 3 places", "the count is what tells the agent how to disambiguate")
	require.Contains(t, text, "replace_all")

	require.Equal(t, before, noteFileBytes(t, dir, "demo", slug),
		"a refused edit must leave the file byte-identical")
}

// replace_all is the documented escape hatch from the ambiguity refusal.
func TestNotesEdit_ReplaceAllChangesEveryOccurrence(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-edit-replace-all"})

	created := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Health probes",
		"body":  "curl :8080/health\ncurl :8080/ready\ncurl :8080/metrics\n",
	})
	slug, _ := created["slug"].(string)

	callJSON(t, ctx, cli, "notes_edit", map[string]any{
		"id": created["id"],
		"edits": []any{map[string]any{
			"old_string": ":8080", "new_string": ":8081", "replace_all": true,
		}},
	})

	onDisk, err := mgr.Store().ReadNote(files.NoteRelPath("demo", slug))
	require.NoError(t, err)
	require.NotContains(t, onDisk.Body, ":8080")
	require.Equal(t, 3, strings.Count(onDisk.Body, ":8081"), "every occurrence must change, not just the first")
}

// Edits apply in order against the running result, and the batch is
// all-or-nothing: a later edit that cannot match discards the earlier ones, so a
// half-applied body never reaches the file. That is the property that lets an
// agent send a multi-edit batch without a rollback plan of its own.
func TestNotesEdit_OrderedAndAllOrNothing(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, dir := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-edit-batch"})

	created := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Batched finding",
		"body":  "alpha\nbeta\ngamma\n",
	})
	noteID, _ := created["id"].(string)
	slug, _ := created["slug"].(string)
	before := noteFileBytes(t, dir, "demo", slug)

	// The first edit matches, the second cannot. Nothing may land -- in particular
	// not the first edit, which applied fine on its own.
	isErr, text := callErr(t, ctx, cli, "notes_edit", map[string]any{
		"id":    noteID,
		"edits": []any{noteEditArg("alpha", "ALPHA"), noteEditArg("delta", "DELTA")},
	})
	require.True(t, isErr, "a failing edit must abort the whole batch: %s", text)
	require.Contains(t, text, "edits[1]", "the message must name which edit failed")
	require.Equal(t, before, noteFileBytes(t, dir, "demo", slug),
		"an aborted batch must leave the file byte-identical, not half-applied")

	// Ordering: the second edit sees the first one's output, which is the only way
	// this pair can succeed at all.
	callJSON(t, ctx, cli, "notes_edit", map[string]any{
		"id":    noteID,
		"edits": []any{noteEditArg("alpha", "delta"), noteEditArg("delta", "DELTA")},
	})
	onDisk, err := mgr.Store().ReadNote(files.NoteRelPath("demo", slug))
	require.NoError(t, err)
	require.Contains(t, onDisk.Body, "DELTA", "edits apply in order to the running result")
	require.NotContains(t, onDisk.Body, "alpha")
	require.Contains(t, onDisk.Body, "beta\ngamma", "the untouched lines must survive the batch")
}

// expect_hash is the guard against an edit computed from a note somebody else
// has since rewritten. The stale call must be REFUSED as a tool error (a success
// payload would leave the agent believing its edit landed) and must leave the
// file untouched; the same call with the current hash goes through.
func TestNotesEdit_ExpectHashPrecondition(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, dir := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-edit-precond"})

	created := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Guarded finding", "body": "the original body\n",
	})
	noteID, _ := created["id"].(string)
	slug, _ := created["slug"].(string)
	stale, _ := callJSON(t, ctx, cli, "notes_read", map[string]any{"id": noteID})["content_hash"].(string)
	require.NotEmpty(t, stale)

	// Somebody else moves the note on: the hash the first agent holds is now stale,
	// while the text it means to edit is still there -- so a refusal can only come
	// from the precondition.
	other := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, other, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-edit-precond-other"})
	callJSON(t, ctx, other, "notes_append", map[string]any{"id": noteID, "body": "a line from the other agent"})
	before := noteFileBytes(t, dir, "demo", slug)

	isErr, text := callErr(t, ctx, cli, "notes_edit", map[string]any{
		"id":          noteID,
		"edits":       []any{noteEditArg("the original body", "the corrected body")},
		"expect_hash": stale,
	})
	require.True(t, isErr, "a stale expect_hash must refuse the edit: %s", text)
	require.Contains(t, text, "expect_hash")
	require.Equal(t, before, noteFileBytes(t, dir, "demo", slug),
		"a refused edit must not touch the file")

	// Re-read, re-apply: with the current hash the same edit lands.
	fresh, _ := callJSON(t, ctx, cli, "notes_read", map[string]any{"id": noteID})["content_hash"].(string)
	require.NotEqual(t, stale, fresh)
	out := callJSON(t, ctx, cli, "notes_edit", map[string]any{
		"id":          noteID,
		"edits":       []any{noteEditArg("the original body", "the corrected body")},
		"expect_hash": fresh,
	})
	onDisk, err := mgr.Store().ReadNote(files.NoteRelPath("demo", slug))
	require.NoError(t, err)
	require.Contains(t, onDisk.Body, "the corrected body")
	require.Contains(t, onDisk.Body, "a line from the other agent", "the other agent's line must survive")
	require.Equal(t, onDisk.ContentHash, out["content_hash"], "the response carries the POST-edit hash")
	require.NotEqual(t, fresh, out["content_hash"])
}

// The regression that matters most: an edit must not quietly discard the
// curation the tool has no arguments for. Tags, the favorite star and the source
// URL are all invisible to an edits[] call, so rendering a fresh note from the
// edited body would erase them while reporting success -- and the star drives
// briefing pinning and the recall boost, so its loss is silent until retrieval
// gets worse.
//
// Everything is asserted against the markdown file, not the index row: files are
// the source of truth for durable knowledge.
func TestNotesEdit_PreservesCuration(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-edit-curation"})

	// Seeded through the files layer: the star and the source URL cannot be
	// expressed through notes_edit at all, which is the whole point of the bug.
	id, err := core.NewID()
	require.NoError(t, err)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	seeded := core.Note{
		ID: id, Title: "Curated finding", Slug: core.Slugify("Curated finding"),
		Description: "seeded description", Project: "demo",
		Body:      "the original body\nwith a second line\n",
		Tags:      []string{"created-by:agent", "plan:dfu-bringup"},
		SourceURL: "https://example.com/source", Favorite: true,
		Created: created,
		Updated: created,
	}
	_, err = mgr.WriteNote(ctx, seeded)
	require.NoError(t, err)

	callJSON(t, ctx, cli, "notes_edit", map[string]any{
		"id":    id,
		"edits": []any{noteEditArg("the original body", "the corrected body")},
	})

	onDisk, err := mgr.Store().ReadNote(files.NoteRelPath("demo", seeded.Slug))
	require.NoError(t, err)

	// The edit did its job.
	require.Contains(t, onDisk.Body, "the corrected body")
	require.Contains(t, onDisk.Body, "with a second line", "the untouched line must survive")

	// ...without collateral damage.
	require.Equal(t, []string{"created-by:agent", "plan:dfu-bringup"}, onDisk.Tags,
		"tags must survive a body edit -- plan:<slug> is what keeps the note in its plan composition")
	require.True(t, onDisk.Favorite,
		"a starred note must stay starred: the star drives briefing pinning and the recall boost")
	require.Equal(t, "https://example.com/source", onDisk.SourceURL, "the source URL must round-trip")

	// Identity and the rest of the metadata are untouched.
	require.Equal(t, id, onDisk.ID)
	require.Equal(t, "Curated finding", onDisk.Title)
	require.Equal(t, "seeded description", onDisk.Description)
	require.Equal(t, "demo", onDisk.Project)
	require.True(t, created.Equal(onDisk.Created), "created: want %s, got %s", created, onDisk.Created)
	require.True(t, onDisk.Updated.After(created), "an edit must bump updated")
}

// Model attribution follows the CONTENT everywhere, so an edit re-stamps it: the
// replacement prose was produced by the model editing it, and leaving the old
// value credits a model that never wrote those words.
func TestNotesEdit_RestampsModel(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)

	writer := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, writer, "session_start", map[string]any{
		"cwd": "/work/demo", "name": "note-edit-writer", "model": "model-alpha",
	})
	created := callJSON(t, ctx, writer, "notes_create", map[string]any{
		"title": "Attributed finding", "body": "written by alpha\n",
	})
	noteID, _ := created["id"].(string)
	relPath := files.NoteRelPath("demo", created["slug"].(string))

	onDisk, err := mgr.Store().ReadNote(relPath)
	require.NoError(t, err)
	require.Equal(t, "model-alpha", onDisk.Model)

	// A different agent on a different model edits it.
	editor := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, editor, "session_start", map[string]any{
		"cwd": "/work/demo", "name": "note-edit-editor", "model": "model-beta",
	})
	callJSON(t, ctx, editor, "notes_edit", map[string]any{
		"id":    noteID,
		"edits": []any{noteEditArg("written by alpha", "edited by beta")},
	})

	onDisk, err = mgr.Store().ReadNote(relPath)
	require.NoError(t, err)
	require.Equal(t, "model-beta", onDisk.Model, "an edit is a body change, so it re-stamps the model")

	// An unknown current model must never erase a known producer with "".
	anon := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, anon, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-edit-anon"})
	callJSON(t, ctx, anon, "notes_edit", map[string]any{
		"id":    noteID,
		"edits": []any{noteEditArg("edited by beta", "edited anonymously")},
	})

	onDisk, err = mgr.Store().ReadNote(relPath)
	require.NoError(t, err)
	require.Equal(t, "model-beta", onDisk.Model, "an unknown model keeps the prior attribution")
	require.Contains(t, onDisk.Body, "edited anonymously")
}

// An edit is a note mutation like any other, so it is a durable domain event --
// and it carries the diff, so the console can reconstruct edit history from the
// log alone rather than from a file that has since moved on.
func TestNotesEdit_RecordsWrittenEvent(t *testing.T) {
	ctx := context.Background()
	url, db, _, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)

	start := callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-edit-events"})
	sessID, _ := start["session_id"].(string)
	require.NotEmpty(t, sessID)

	created := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Event finding", "body": "the original body\n",
	})
	noteID, _ := created["id"].(string)

	callJSON(t, ctx, cli, "notes_edit", map[string]any{
		"id":    noteID,
		"edits": []any{noteEditArg("the original body", "the corrected body")},
	})

	written := noteEventRows(t, db, core.EventNoteWritten)
	require.Len(t, written, 2, "the create and the edit are each a note.written")
	ev := written[1]
	require.Equal(t, noteID, ev["_item"], "the event must name the note it changed")
	require.Equal(t, "demo", ev["_project"])
	require.Equal(t, sessID, ev["_session"], "the event must carry the editing session")
	require.Equal(t, true, ev["edited"], "an edit must be discriminable from a create, an update and an append")
	require.Nil(t, ev["updated"])
	require.Nil(t, ev["appended"])
	require.Equal(t, "Event finding", ev["title"])
	diff, _ := ev["diff"].(string)
	require.Contains(t, diff, "-the original body")
	require.Contains(t, diff, "+the corrected body")

	// A refused edit is not a write, so it must leave no event behind.
	isErr, _ := callErr(t, ctx, cli, "notes_edit", map[string]any{
		"id":    noteID,
		"edits": []any{noteEditArg("no such text", "whatever")},
	})
	require.True(t, isErr)
	require.Len(t, noteEventRows(t, db, core.EventNoteWritten), 2, "a refused edit must not record a write")
}

// Two agents editing disjoint regions of one note is the normal case for
// parallel subagents, not the pathological one. Unserialized, both would read the
// same starting body and the second rename would keep only its own change, with
// no error anywhere. Each editor gets its own client so these are separate bound
// sessions rather than one agent talking to itself.
func TestNotesEdit_ConcurrentEditsLoseNeither(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	seeder := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, seeder, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-edit-race-seed"})
	created := callJSON(t, ctx, seeder, "notes_create", map[string]any{
		"title": "Contended finding",
		"body":  "alpha-token holds the first half\nbeta-token holds the second half\n",
	})
	noteID, _ := created["id"].(string)
	slug, _ := created["slug"].(string)

	const editors = 2
	tokens := [editors]string{"alpha-token", "beta-token"}
	clients := make([]*mcpclient.Client, editors)
	for i := range clients {
		clients[i] = dialClient(t, ctx, url, testKey)
		callJSON(t, ctx, clients[i], "session_start", map[string]any{
			"cwd": "/work/demo", "name": "note-edit-race-" + tokens[i],
		})
	}

	// One release for everyone, so the calls overlap instead of queueing behind
	// each other's setup.
	start := make(chan struct{})
	errs := make([]error, editors)
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = callToolAsync(ctx, clients[i], "notes_edit", map[string]any{
				"id":    noteID,
				"edits": []any{noteEditArg(tokens[i], strings.ToUpper(tokens[i]))},
			})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "editor %d", i)
	}

	// The file is the source of truth, so assert there rather than on the index.
	onDisk, err := mgr.Store().ReadNote(files.NoteRelPath("demo", slug))
	require.NoError(t, err)
	for _, tok := range tokens {
		require.Contains(t, onDisk.Body, strings.ToUpper(tok),
			"edit of %q was overwritten by the concurrent one -- the lost update is back", tok)
		require.NotContains(t, onDisk.Body, tok)
	}
}

// An id that resolves to nothing is a tool error pointing at the addressing that
// does work, never a panic and never a success payload for a write that never
// happened.
func TestNotesEdit_UnknownIDIsToolError(t *testing.T) {
	ctx := context.Background()
	url, _, _, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-edit-miss"})

	isErr, text := callErr(t, ctx, cli, "notes_edit", map[string]any{
		"id":    "01JZZZZZZZZZZZZZZZZZZZZZZZ",
		"edits": []any{noteEditArg("anything", "something")},
	})
	require.True(t, isErr, "an unknown id must be a tool error: %s", text)
	require.Contains(t, text, "no note with id")
	require.Contains(t, text, "recall", "the message must point at the addressing that does work")

	// A missing id is a usage error too, not an edit of whatever happened to be first.
	isErr, text = callErr(t, ctx, cli, "notes_edit", map[string]any{
		"edits": []any{noteEditArg("anything", "something")},
	})
	require.True(t, isErr, "a missing id must be refused: %s", text)
}
