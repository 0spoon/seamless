package mcp_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/files"
)

// The lost update, on the notes side. notes_append used to read the file, edit
// the struct, and rename a whole new file over it; two agents appending to one
// note both read the same starting body and the second rename kept only its own
// line, with no error anywhere -- the index upsert is keyed by id, so even the
// UNIQUE file_path constraint stayed quiet.
//
// Each appender gets its own MCP client, which is what makes these separate
// bound sessions rather than one agent talking to itself: this is the shape the
// bug actually took (parallel subagents sharing an investigation log).
func TestNotesAppend_ConcurrentAppendsLoseNothing(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)

	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-race-seed"})
	created := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Investigation log", "body": "base line",
	})
	noteID, _ := created["id"].(string)
	require.NotEmpty(t, noteID)
	slug, _ := created["slug"].(string)

	const appenders = 8
	clients := make([]*mcpclient.Client, appenders)
	for i := range clients {
		clients[i] = dialClient(t, ctx, url, testKey)
		callJSON(t, ctx, clients[i], "session_start", map[string]any{
			"cwd": "/work/demo", "name": fmt.Sprintf("note-race-%02d", i),
		})
	}

	// One release for everyone, so the calls actually overlap instead of queueing
	// behind each other's setup.
	start := make(chan struct{})
	errs := make([]error, appenders)
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = callToolAsync(ctx, clients[i], "notes_append", map[string]any{
				"id": noteID, "body": fmt.Sprintf("finding-%02d", i),
			})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "appender %d", i)
	}

	// The file is the source of truth, so assert there rather than on the index.
	onDisk, err := mgr.Store().ReadNote(files.NoteRelPath("demo", slug))
	require.NoError(t, err)
	require.Contains(t, onDisk.Body, "base line", "the starting body must survive every append")
	for i := range appenders {
		require.Contains(t, onDisk.Body, fmt.Sprintf("finding-%02d", i),
			"append %d was overwritten by a concurrent one -- the lost update is back", i)
	}
}

// notes_update rewrites the whole file from a read taken moments earlier, so
// concurrent updates must serialize: each one lands whole, and none may pair one
// caller's title with another's description. Which caller wins is unknowable and
// fine; a torn write is not.
func TestNotesUpdate_ConcurrentUpdatesSerialize(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-update-seed"})

	created := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Contended", "body": "the original body",
	})
	noteID, _ := created["id"].(string)
	slug, _ := created["slug"].(string)

	const writers = 8
	clients := make([]*mcpclient.Client, writers)
	for i := range clients {
		clients[i] = dialClient(t, ctx, url, testKey)
		callJSON(t, ctx, clients[i], "session_start", map[string]any{
			"cwd": "/work/demo", "name": fmt.Sprintf("note-update-%02d", i),
		})
	}

	start := make(chan struct{})
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = callToolAsync(ctx, clients[i], "notes_update", map[string]any{
				"id":          noteID,
				"description": fmt.Sprintf("desc-%02d", i),
				"body":        fmt.Sprintf("body-%02d", i),
			})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "writer %d", i)
	}

	onDisk, err := mgr.Store().ReadNote(files.NoteRelPath("demo", slug))
	require.NoError(t, err)
	body := strings.TrimSpace(onDisk.Body)
	require.Regexp(t, `^body-\d{2}$`, body, "the file must hold exactly one writer's body, whole")
	require.Equal(t, "desc-"+strings.TrimPrefix(body, "body-"), onDisk.Description,
		"body and description must come from the same update")
	require.Equal(t, noteID, onDisk.ID, "identity must survive a contended update")
}

// A move writes the new path and removes the old one, and both halves run under
// one lock over BOTH paths. The observable contract is what this asserts: the
// note exists exactly once afterwards, at its new path, with its id and slug
// intact -- and a target slug already owned by a different note is refused
// before anything is written.
func TestNotesUpdate_ProjectMove(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, dir := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-move-sess"})

	created := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Movable finding", "body": "the body that travels",
	})
	noteID, _ := created["id"].(string)
	slug, _ := created["slug"].(string)

	callJSON(t, ctx, cli, "notes_update", map[string]any{"id": noteID, "project": "other"})

	// The file moved: present at the new path, gone from the old one.
	moved, err := mgr.Store().ReadNote(files.NoteRelPath("other", slug))
	require.NoError(t, err)
	require.Equal(t, noteID, moved.ID)
	require.Equal(t, "other", moved.Project)
	require.Contains(t, moved.Body, "the body that travels", "a move must not lose the body")

	_, err = os.Lstat(filepath.Join(dir, filepath.FromSlash(files.NoteRelPath("demo", slug))))
	require.True(t, os.IsNotExist(err), "the old file must be removed, not left as a duplicate")

	// And the index followed, so the note reads back from its new scope.
	read := callJSON(t, ctx, cli, "notes_read", map[string]any{"id": noteID})
	require.Equal(t, "other", read["project"])
	require.Equal(t, slug, read["slug"], "the slug is id-addressed and stays stable across a move")

	// A different note already owning the target slug is a refusal, and the
	// would-be mover stays exactly where it was.
	other := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Movable finding", "body": "a namesake in demo", "project": "demo",
	})
	isErr, text := callErr(t, ctx, cli, "notes_update", map[string]any{
		"id": other["id"], "project": "other",
	})
	require.True(t, isErr, "a slug collision in the target project must refuse the move: %s", text)
	require.Contains(t, text, "already exists")
	still, err := mgr.Store().ReadNote(files.NoteRelPath("demo", slug))
	require.NoError(t, err)
	require.Equal(t, other["id"], still.ID, "the refused move must leave the note at its old path")
}

// Model attribution follows the CONTENT everywhere, so an append re-stamps it:
// the prose was produced by the model appending it, and leaving the create-time
// value credits a model that never wrote those lines. SourceSession has no
// note-side field at all, so the model is the whole attribution here.
func TestNotesAppend_RestampsModel(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)

	writer := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, writer, "session_start", map[string]any{
		"cwd": "/work/demo", "name": "note-attrib-writer", "model": "model-alpha",
	})
	created := callJSON(t, ctx, writer, "notes_create", map[string]any{
		"title": "Attributed note", "body": "written by alpha",
	})
	noteID, _ := created["id"].(string)
	relPath := files.NoteRelPath("demo", created["slug"].(string))

	onDisk, err := mgr.Store().ReadNote(relPath)
	require.NoError(t, err)
	require.Equal(t, "model-alpha", onDisk.Model)

	// A different agent on a different model appends.
	appender := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, appender, "session_start", map[string]any{
		"cwd": "/work/demo", "name": "note-attrib-appender", "model": "model-beta",
	})
	callJSON(t, ctx, appender, "notes_append", map[string]any{"id": noteID, "body": "appended by beta"})

	onDisk, err = mgr.Store().ReadNote(relPath)
	require.NoError(t, err)
	require.Equal(t, "model-beta", onDisk.Model, "an append is a body change, so it re-stamps the model")
	require.Contains(t, onDisk.Body, "written by alpha")
	require.Contains(t, onDisk.Body, "appended by beta")

	// An unknown current model must never erase a known producer with "".
	anon := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, anon, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-attrib-anon"})
	callJSON(t, ctx, anon, "notes_append", map[string]any{"id": noteID, "body": "appended anonymously"})

	onDisk, err = mgr.Store().ReadNote(relPath)
	require.NoError(t, err)
	require.Equal(t, "model-beta", onDisk.Model, "an unknown model keeps the prior attribution")
	require.Contains(t, onDisk.Body, "appended anonymously")
}

// content_hash is the ETag half of the expect_hash precondition: an agent can
// only say "write only if nothing moved since I read this" if the read handed it
// the hash. It must be the digest of the FILE's bytes, since that is what the
// precondition compares against inside the mutation lock.
func TestNotesRead_ReturnsFileContentHash(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, dir := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-hash-sess"})

	created := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Hashed note", "body": "first body",
	})
	noteID, _ := created["id"].(string)
	slug, _ := created["slug"].(string)
	relPath := files.NoteRelPath("demo", slug)
	absPath := filepath.Join(dir, filepath.FromSlash(relPath))

	// Hash the raw file the same way the reconciler does, so this asserts the
	// contract rather than agreeing with whatever the read happened to compute.
	rawHash := func() string {
		t.Helper()
		raw, err := os.ReadFile(absPath) //nolint:gosec // test-owned path under t.TempDir()
		require.NoError(t, err)
		return files.ContentHash(string(raw))
	}

	read := callJSON(t, ctx, cli, "notes_read", map[string]any{"id": noteID})
	first, ok := read["content_hash"].(string)
	require.True(t, ok, "notes_read must return content_hash, or expect_hash has nothing to compare against")
	require.NotEmpty(t, first)
	require.Equal(t, rawHash(), first)

	onDisk, err := mgr.Store().ReadNote(relPath)
	require.NoError(t, err)
	require.Equal(t, onDisk.ContentHash, first)

	// A body change moves it, and the by-slug read is the same read.
	callJSON(t, ctx, cli, "notes_append", map[string]any{"id": noteID, "body": "a second line"})
	bySlug := callJSON(t, ctx, cli, "notes_read", map[string]any{"slug": slug})
	require.Equal(t, rawHash(), bySlug["content_hash"])
	require.NotEqual(t, first, bySlug["content_hash"], "an append must move the hash")
}

// expect_hash is the guard against a write computed from a note somebody else
// has since rewritten. The stale call must be REFUSED as a tool error (a success
// payload would leave the agent believing its edit landed) and must leave the
// file untouched; the same call with the current hash goes through.
func TestNotesUpdate_ExpectHashRefusesStaleWrite(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-precond-sess"})

	created := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Guarded note", "body": "the original body",
	})
	noteID, _ := created["id"].(string)
	relPath := files.NoteRelPath("demo", created["slug"].(string))

	stale := callJSON(t, ctx, cli, "notes_read", map[string]any{"id": noteID})["content_hash"].(string)

	// Somebody else moves the note on: the hash the first agent holds is now stale.
	other := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, other, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-precond-other"})
	callJSON(t, ctx, other, "notes_append", map[string]any{"id": noteID, "body": "a line from the other agent"})

	before, err := mgr.Store().ReadNote(relPath)
	require.NoError(t, err)

	isErr, text := callErr(t, ctx, cli, "notes_update", map[string]any{
		"id": noteID, "body": "a full-body replace computed from the stale read", "expect_hash": stale,
	})
	require.True(t, isErr, "a stale expect_hash must refuse the write: %s", text)
	require.Contains(t, text, "expect_hash")

	after, err := mgr.Store().ReadNote(relPath)
	require.NoError(t, err)
	require.Equal(t, before.ContentHash, after.ContentHash, "a refused write must not touch the file")
	require.Contains(t, after.Body, "a line from the other agent")

	// A precondition the caller stated is enforced even with no body: a
	// metadata-only edit computed from a note that has since changed is the same
	// stale-context write, and silently ignoring the guard is worse than refusing.
	isErr, text = callErr(t, ctx, cli, "notes_update", map[string]any{
		"id": noteID, "description": "a metadata-only edit", "expect_hash": stale,
	})
	require.True(t, isErr, "expect_hash must be honored without a body too: %s", text)
	require.Contains(t, text, "expect_hash")

	// Re-read, re-apply: with the current hash the same write lands.
	fresh := callJSON(t, ctx, cli, "notes_read", map[string]any{"id": noteID})["content_hash"].(string)
	require.NotEqual(t, stale, fresh)
	callJSON(t, ctx, cli, "notes_update", map[string]any{
		"id": noteID, "body": "the re-applied body", "expect_hash": fresh,
	})
	final, err := mgr.Store().ReadNote(relPath)
	require.NoError(t, err)
	require.Equal(t, "the re-applied body", strings.TrimSpace(final.Body))

	// And an omitted precondition still writes unconditionally, which is what
	// keeps every pre-existing caller working.
	callJSON(t, ctx, cli, "notes_update", map[string]any{"id": noteID, "body": "written with no precondition"})
	final, err = mgr.Store().ReadNote(relPath)
	require.NoError(t, err)
	require.Equal(t, "written with no precondition", strings.TrimSpace(final.Body))
}

// tags_add/tags_remove edit the set in place, which is what a concurrent agent
// needs (a full replace discards whatever landed in between) and is the only way
// to CLEAR a tag at all -- an empty tags array reads as absent. When more than
// one tag argument is sent they compose in one fixed order: replace, add, remove.
func TestNotesUpdate_TagAddAndRemove(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "note-tags-sess"})

	created := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Tagged note", "body": "b", "tags": []any{"alpha", "beta"},
	})
	noteID, _ := created["id"].(string)
	relPath := files.NoteRelPath("demo", created["slug"].(string))
	tagsOf := func() []string {
		t.Helper()
		onDisk, err := mgr.Store().ReadNote(relPath)
		require.NoError(t, err)
		return onDisk.Tags
	}
	require.ElementsMatch(t, []string{"alpha", "beta", "created-by:agent"}, tagsOf())

	// Adding leaves the rest in place, and re-adding an existing tag does not
	// duplicate it.
	callJSON(t, ctx, cli, "notes_update", map[string]any{
		"id": noteID, "tags_add": []any{"gamma", "alpha"},
	})
	require.ElementsMatch(t, []string{"alpha", "beta", "created-by:agent", "gamma"}, tagsOf())

	// Removing is exact-match, and a tag the note does not carry is ignored.
	callJSON(t, ctx, cli, "notes_update", map[string]any{
		"id": noteID, "tags_remove": []any{"beta", "never-there"},
	})
	require.ElementsMatch(t, []string{"alpha", "created-by:agent", "gamma"}, tagsOf())

	// All three in one call compose in a fixed order: replace, then add, then
	// remove -- so "delta" survives and "alpha" does not.
	callJSON(t, ctx, cli, "notes_update", map[string]any{
		"id": noteID, "tags": []any{"alpha"}, "tags_add": []any{"delta"}, "tags_remove": []any{"alpha"},
	})
	require.ElementsMatch(t, []string{"delta"}, tagsOf())

	// The last tag can be cleared, which the full-list parameter cannot express.
	callJSON(t, ctx, cli, "notes_update", map[string]any{"id": noteID, "tags_remove": []any{"delta"}})
	require.Empty(t, tagsOf(), "tags_remove must be able to clear the final tag")

	// The full-list parameter's contract is unchanged: an empty array is absent,
	// not a clear.
	callJSON(t, ctx, cli, "notes_update", map[string]any{"id": noteID, "tags": []any{"kept"}})
	callJSON(t, ctx, cli, "notes_update", map[string]any{"id": noteID, "tags": []any{}, "description": "d"})
	require.ElementsMatch(t, []string{"kept"}, tagsOf(), "an empty tags list is read as absent, not as a clear")

	// A call that asks for nothing at all is still a usage error -- expect_hash is
	// a precondition ON a change, not a change.
	isErr, text := callErr(t, ctx, cli, "notes_update", map[string]any{"id": noteID})
	require.True(t, isErr)
	require.Contains(t, text, "at least one field")
}
