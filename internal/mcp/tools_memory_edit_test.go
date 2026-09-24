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

	"github.com/0spoon/seamless/internal/agentguide"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/files"
)

// memoryFileBytes reads a memory's markdown file exactly as it sits on disk.
// The byte comparison is the point: "the refused edit changed nothing" is a
// claim about the file, and asserting on a parsed struct would miss frontmatter
// the parser normalizes away.
func memoryFileBytes(t *testing.T, dir, project, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(files.MemoryRelPath(project, name)))) //nolint:gosec // test-owned path under t.TempDir()
	require.NoError(t, err)
	return raw
}

// editArg builds one element of the edits[] argument.
func editArg(oldStr, newStr string) map[string]any {
	return map[string]any{"old_string": oldStr, "new_string": newStr}
}

// A unique match changes exactly what it names and nothing around it, and the
// returned content_hash is the FILE's -- the handle the expect_hash precondition
// is checked against, so a hash describing anything else would make the
// precondition inert.
func TestMemoryEdit_UniqueEditChangesOnlyItsTarget(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, dir := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "edit-unique"})

	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "port-runbook", "kind": "runbook", "description": "how the daemon binds",
		"body": "The daemon binds 127.0.0.1:8080 by default.\nRestart it with launchctl kickstart.\nThe data dir is ~/.seamless.\n",
	})

	out := callJSON(t, ctx, cli, "memory_edit", map[string]any{
		"name":  "port-runbook",
		"edits": []any{editArg("127.0.0.1:8080", "127.0.0.1:8081")},
	})
	require.Equal(t, "demo", out["project"])

	onDisk, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "port-runbook"))
	require.NoError(t, err)
	require.Contains(t, onDisk.Body, "127.0.0.1:8081")
	require.NotContains(t, onDisk.Body, "8080", "the replaced text must be gone")
	require.Contains(t, onDisk.Body, "Restart it with launchctl kickstart.", "untouched lines must survive verbatim")
	require.Contains(t, onDisk.Body, "The data dir is ~/.seamless.")

	require.Equal(t, files.ContentHash(string(memoryFileBytes(t, dir, "demo", "port-runbook"))), out["content_hash"],
		"content_hash must be the file's, so a follow-up expect_hash matches")

	diff, _ := out["diff"].(string)
	require.Contains(t, diff, "-The daemon binds 127.0.0.1:8080 by default.")
	require.Contains(t, diff, "+The daemon binds 127.0.0.1:8081 by default.")
}

// The unique-or-fail guard is the whole safety story at the tool surface: an
// ambiguous old_string is refused with the count (so the agent knows what to do
// about it), and the file is byte-identical afterwards.
func TestMemoryEdit_AmbiguousMatchRefusesAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	url, _, _, dir := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "edit-ambiguous"})

	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "checklist", "kind": "runbook", "description": "the release checklist",
		"body": "TODO wire the flag\nTODO write the test\nTODO update the docs\n",
	})
	before := memoryFileBytes(t, dir, "demo", "checklist")

	isErr, text := callErr(t, ctx, cli, "memory_edit", map[string]any{
		"name":  "checklist",
		"edits": []any{editArg("TODO", "DONE")},
	})
	require.True(t, isErr, "an ambiguous old_string must be refused: %s", text)
	require.Contains(t, text, "matches 3 places", "the count is what tells the agent how to disambiguate")
	require.Contains(t, text, "replace_all")

	require.Equal(t, before, memoryFileBytes(t, dir, "demo", "checklist"),
		"a refused edit must leave the file byte-identical")
}

// replace_all is the documented escape hatch from the ambiguity refusal.
func TestMemoryEdit_ReplaceAllChangesEveryOccurrence(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "edit-replace-all"})

	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "old-port", "kind": "reference", "description": "the old port everywhere",
		"body": "curl :8080/health\ncurl :8080/ready\ncurl :8080/metrics\n",
	})

	callJSON(t, ctx, cli, "memory_edit", map[string]any{
		"name": "old-port",
		"edits": []any{map[string]any{
			"old_string": ":8080", "new_string": ":8081", "replace_all": true,
		}},
	})

	onDisk, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "old-port"))
	require.NoError(t, err)
	require.NotContains(t, onDisk.Body, ":8080")
	require.Equal(t, 3, strings.Count(onDisk.Body, ":8081"), "every occurrence must change, not just the first")
}

// Edits apply in order against the running result, and the batch is
// all-or-nothing: a later edit that cannot match discards the earlier ones, so a
// half-applied body never reaches the file. That is the property that lets an
// agent send a multi-edit batch without a rollback plan of its own.
func TestMemoryEdit_OrderedAndAllOrNothing(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, dir := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "edit-batch"})

	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "batched", "kind": "gotcha", "description": "three lines",
		"body": "alpha\nbeta\ngamma\n",
	})
	before := memoryFileBytes(t, dir, "demo", "batched")

	// The first edit matches, the second cannot. Nothing may land.
	isErr, text := callErr(t, ctx, cli, "memory_edit", map[string]any{
		"name":  "batched",
		"edits": []any{editArg("alpha", "ALPHA"), editArg("nowhere", "x")},
	})
	require.True(t, isErr, "a failing later edit must fail the whole batch: %s", text)
	require.Contains(t, text, "edits[1]", "the error must name which edit missed")
	require.Equal(t, before, memoryFileBytes(t, dir, "demo", "batched"),
		"the earlier, matching edit must not have landed either")

	// Applied in order against the running result: the second edit sees the
	// first's output, which is how overlapping changes are expressed as a pair.
	callJSON(t, ctx, cli, "memory_edit", map[string]any{
		"name":  "batched",
		"edits": []any{editArg("alpha", "ALPHA"), editArg("ALPHA", "OMEGA"), editArg("gamma", "GAMMA")},
	})
	onDisk, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "batched"))
	require.NoError(t, err)
	require.Contains(t, onDisk.Body, "OMEGA", "edits must see each other's output, in order")
	require.NotContains(t, onDisk.Body, "ALPHA")
	require.Contains(t, onDisk.Body, "beta", "the untouched line stays")
	require.Contains(t, onDisk.Body, "GAMMA")
}

// expect_hash is the optimistic-concurrency half: a caller acting on a body it
// read minutes ago is refused rather than silently overwriting what moved in
// between, and the hash it just received back is immediately usable.
func TestMemoryEdit_ExpectHashPrecondition(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "edit-precondition"})

	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "guarded", "kind": "decision", "description": "guarded by a hash",
		"body": "the first claim\nthe second claim\n",
	})
	read := callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "guarded"})
	stale, _ := read["content_hash"].(string)
	require.NotEmpty(t, stale)

	// A hash that never described this file is refused outright.
	isErr, text := callErr(t, ctx, cli, "memory_edit", map[string]any{
		"name":        "guarded",
		"edits":       []any{editArg("the first claim", "the corrected claim")},
		"expect_hash": strings.Repeat("0", 64),
	})
	require.True(t, isErr, "a mismatched expect_hash must refuse the write: %s", text)
	require.Contains(t, text, "expect_hash")

	// The hash from the read succeeds and reports the new one.
	out := callJSON(t, ctx, cli, "memory_edit", map[string]any{
		"name":        "guarded",
		"edits":       []any{editArg("the first claim", "the corrected claim")},
		"expect_hash": stale,
	})
	fresh, _ := out["content_hash"].(string)
	require.NotEmpty(t, fresh)
	require.NotEqual(t, stale, fresh, "a landed edit must move the content hash")

	// The now-stale hash is refused: this is the same agent's second call, which
	// is exactly the "I am acting on what I read earlier" case.
	isErr, text = callErr(t, ctx, cli, "memory_edit", map[string]any{
		"name":        "guarded",
		"edits":       []any{editArg("the second claim", "another correction")},
		"expect_hash": stale,
	})
	require.True(t, isErr, "a stale expect_hash must refuse the write: %s", text)

	onDisk, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "guarded"))
	require.NoError(t, err)
	require.Contains(t, onDisk.Body, "the corrected claim")
	require.Contains(t, onDisk.Body, "the second claim", "the refused edit must not have landed")
	require.Equal(t, fresh, onDisk.ContentHash)
}

// The regression that matters most. An edit rewrites the whole markdown file
// from the struct it was handed, so everything the tool has no argument for --
// the favorite star that drives briefing pinning and the recall boost, and the
// unknown frontmatter keys Extra round-trips, which have no second copy anywhere
// (Extra is deliberately not mirrored to the index) -- has to ride along
// untouched. It does because the handler edits the memory it just READ instead
// of building a fresh one.
func TestMemoryEdit_PreservesCuration(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "edit-preserve"})

	// Seeded through the files layer: tags, the star and unknown keys cannot all
	// be expressed through the tool surface, which is what made losing them
	// invisible in the first place.
	id, err := core.NewID()
	require.NoError(t, err)
	created := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	seeded := core.Memory{
		ID: id, Kind: core.KindGotcha, Name: "curated-edit", Description: "seeded description",
		Project: "demo", Body: "the original claim about the fixture\n",
		Tags:     []string{"created-by:agent", "topic:dfu"},
		Favorite: true, Extra: map[string]any{"obsidian_plugin": "dataview", "owner_pinned": true},
		Created: created, Updated: created, ValidFrom: created,
	}
	_, err = mgr.WriteMemory(ctx, seeded)
	require.NoError(t, err)

	out := callJSON(t, ctx, cli, "memory_edit", map[string]any{
		"name":  "curated-edit",
		"edits": []any{editArg("the original claim", "the corrected claim")},
	})
	require.Equal(t, id, out["id"], "an edit must not fork identity")

	onDisk, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "curated-edit"))
	require.NoError(t, err)
	require.Contains(t, onDisk.Body, "the corrected claim")

	require.Equal(t, []string{"created-by:agent", "topic:dfu"}, onDisk.Tags, "tags must survive a body edit")
	require.True(t, onDisk.Favorite, "a starred memory must stay starred")
	require.Equal(t, "dataview", onDisk.Extra["obsidian_plugin"], "unknown frontmatter keys must round-trip")
	require.Equal(t, true, onDisk.Extra["owner_pinned"])

	// Identity, kind and creation provenance are not content and do not move.
	require.Equal(t, id, onDisk.ID)
	require.Equal(t, core.KindGotcha, onDisk.Kind)
	require.Equal(t, "seeded description", onDisk.Description, "an edit that sends no description leaves it alone")
	require.True(t, created.Equal(onDisk.Created), "created: want %s, got %s", created, onDisk.Created)
	require.True(t, onDisk.Updated.After(created), "updated must be bumped")
}

// description, tags_add and tags_remove are the metadata half. tags_remove is
// what finally makes CLEARING expressible: memory_write's `tags` replaces the
// whole set and an empty array reads as absent, so "no tags" cannot be said
// through it at all.
func TestMemoryEdit_DescriptionAndTags(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "edit-metadata"})

	tagsOf := func(name string) []string {
		t.Helper()
		onDisk, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", name))
		require.NoError(t, err)
		return onDisk.Tags
	}

	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "metadata", "kind": "convention", "description": "the first description",
		"body": "line one\nline two\n", "tags": []any{"alpha", "beta"},
	})

	// One call carrying every metadata argument: add is deduped against what is
	// already there, and remove runs after add.
	callJSON(t, ctx, cli, "memory_edit", map[string]any{
		"name":        "metadata",
		"edits":       []any{editArg("line one", "line 1")},
		"description": "the corrected description",
		"tags_add":    []any{"gamma", "alpha"},
		"tags_remove": []any{"beta"},
	})
	onDisk, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "metadata"))
	require.NoError(t, err)
	require.Equal(t, "the corrected description", onDisk.Description)
	require.Contains(t, onDisk.Body, "line 1")
	require.Equal(t, []string{"alpha", "gamma"}, onDisk.Tags, "add dedupes, remove is exact-match, and both apply to the current set")

	// Omitting the metadata arguments leaves them alone -- the same "absent is not
	// a clear" contract memory_write's preservation path enforces.
	callJSON(t, ctx, cli, "memory_edit", map[string]any{
		"name":  "metadata",
		"edits": []any{editArg("line two", "line 2")},
	})
	onDisk, err = mgr.Store().ReadMemory(files.MemoryRelPath("demo", "metadata"))
	require.NoError(t, err)
	require.Equal(t, "the corrected description", onDisk.Description, "omitting description must not clear it")
	require.Equal(t, []string{"alpha", "gamma"}, tagsOf("metadata"), "omitting the tag arguments must not clear them")

	// Removing the last tags clears the set, which is the whole point of the
	// parameter existing.
	callJSON(t, ctx, cli, "memory_edit", map[string]any{
		"name":        "metadata",
		"edits":       []any{editArg("line 2", "line two")},
		"tags_remove": []any{"alpha", "gamma"},
	})
	require.Empty(t, tagsOf("metadata"), "tags_remove must be able to clear the last tag")

	// An over-long description is word-truncated exactly as memory_write caps it,
	// so an edited description can never end mid-word in the indexes.
	long := strings.TrimSpace(strings.Repeat("verbose ", 40))
	callJSON(t, ctx, cli, "memory_edit", map[string]any{
		"name":        "metadata",
		"edits":       []any{editArg("line two", "line 2")},
		"description": long,
	})
	onDisk, err = mgr.Store().ReadMemory(files.MemoryRelPath("demo", "metadata"))
	require.NoError(t, err)
	require.LessOrEqual(t, len([]rune(onDisk.Description)), 150)
	require.Less(t, len(onDisk.Description), len(long))
}

// The stage hint is recomputed from the POST-edit body, because flipping a
// stage's Status is one of the corrections this tool exists for: an edit that
// repairs the header must not still be nagged about, and one that leaves the
// stage headerless must be, since a headerless stage renders as status unknown
// and ages out of the briefing instead of pinning.
func TestMemoryEdit_StageHintFollowsTheEditedBody(t *testing.T) {
	ctx := context.Background()
	url, _, _, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "edit-stage"})

	// A broken header, repaired by the edit: no hint.
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "vendor-wait", "kind": "stage", "description": "waiting on the vendor",
		"body": "Status: wip\n\nthe vendor has the repro\n",
	})
	out := callJSON(t, ctx, cli, "memory_edit", map[string]any{
		"name":  "vendor-wait",
		"edits": []any{editArg("Status: wip", "Status: blocked\nGate: human")},
	})
	require.NotContains(t, out, "stage_hint", "an edit that repairs the Status header must silence the hint")

	// A headerless stage, still headerless after the edit: the hint fires and
	// teaches the shared contract verbatim rather than a paraphrase.
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "review-wait", "kind": "stage", "description": "waiting on review",
		"body": "PR 123 awaits maintainer re-review\n",
	})
	out = callJSON(t, ctx, cli, "memory_edit", map[string]any{
		"name":  "review-wait",
		"edits": []any{editArg("PR 123", "PR 456")},
	})
	hint, _ := out["stage_hint"].(string)
	require.Contains(t, hint, "no parseable Status header")
	require.Contains(t, hint, agentguide.StageContract, "the hint teaches the shared contract, not a paraphrase")

	// A non-stage kind never hints, whatever the body looks like.
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "plain", "kind": "gotcha", "description": "no headers here", "body": "a trap\n",
	})
	out = callJSON(t, ctx, cli, "memory_edit", map[string]any{
		"name":  "plain",
		"edits": []any{editArg("a trap", "a subtler trap")},
	})
	require.NotContains(t, out, "stage_hint")
}

// Two agents editing disjoint regions of one memory is the normal case for
// parallel subagents, not the pathological one. Unserialized, both would read
// the same starting body and the second rename would keep only its own change,
// with no error anywhere. Each editor gets its own client so these are separate
// bound sessions rather than one agent talking to itself.
func TestMemoryEdit_ConcurrentEditsLoseNeither(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	seeder := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, seeder, "session_start", map[string]any{"cwd": "/work/demo", "name": "edit-race-seed"})
	callJSON(t, ctx, seeder, "memory_write", map[string]any{
		"name": "contended-edit", "kind": "runbook", "description": "two disjoint regions",
		"body": "alpha-token holds the first half\nbeta-token holds the second half\n",
	})

	const editors = 2
	tokens := [editors]string{"alpha-token", "beta-token"}
	clients := make([]*mcpclient.Client, editors)
	for i := range clients {
		clients[i] = dialClient(t, ctx, url, testKey)
		callJSON(t, ctx, clients[i], "session_start", map[string]any{
			"cwd": "/work/demo", "name": "edit-race-" + tokens[i],
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
			errs[i] = callToolAsync(ctx, clients[i], "memory_edit", map[string]any{
				"name":  "contended-edit",
				"edits": []any{editArg(tokens[i], strings.ToUpper(tokens[i]))},
			})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "editor %d", i)
	}

	// The file is the source of truth, so assert there rather than on the index.
	onDisk, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "contended-edit"))
	require.NoError(t, err)
	for _, tok := range tokens {
		require.Contains(t, onDisk.Body, strings.ToUpper(tok),
			"edit of %q was overwritten by the concurrent one -- the lost update is back", tok)
		require.NotContains(t, onDisk.Body, tok)
	}
}

// The metadata-only call: description and tags changed with NO body edit.
//
// This is the gap memory_edit exists to close, and it is the one the schema
// nearly re-opened. memory_write requires a body, so before this tool there was
// no way to fix a memory's one-line description -- the only text every index and
// briefing shows -- or to clear a tag without resending the whole body and
// risking the curation carry-forward. edits[] is therefore deliberately NOT
// required on this tool; the handler's own guard covers the call that sends
// nothing at all.
func TestMemoryEdit_MetadataOnlyLeavesTheBodyAlone(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "edit-metadata-only"})

	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "meta-only", "kind": "gotcha", "description": "typo in teh description",
		"body": "the body must not move", "tags": []string{"keep", "drop"},
	})
	before, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "meta-only"))
	require.NoError(t, err)

	out := callJSON(t, ctx, cli, "memory_edit", map[string]any{
		"name":        "meta-only",
		"description": "typo in the description, fixed",
		"tags_add":    []string{"added"},
		"tags_remove": []string{"drop"},
	})
	require.NotEmpty(t, out["content_hash"])
	require.NotContains(t, out, "diff", "a metadata-only edit changed no body, so there is nothing to diff")

	after, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "meta-only"))
	require.NoError(t, err)
	require.Equal(t, "typo in the description, fixed", after.Description)
	require.ElementsMatch(t, []string{"keep", "added"}, after.Tags)
	require.Equal(t, before.Body, after.Body, "no edits were sent, so the body must be byte-identical")
	require.Equal(t, before.ID, after.ID)
	require.NotEqual(t, before.ContentHash, after.ContentHash, "the frontmatter changed, so the file hash must move")
}

// A call that names neither an edit nor any metadata is told so, rather than
// reporting a write it never made.
func TestMemoryEdit_NothingToChangeIsAnError(t *testing.T) {
	ctx := context.Background()
	url, _, _, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "edit-empty"})
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "untouched", "kind": "gotcha", "description": "d", "body": "b",
	})

	isErr, msg := callErr(t, ctx, cli, "memory_edit", map[string]any{"name": "untouched"})
	require.True(t, isErr)
	require.Contains(t, msg, "nothing to change")
}
