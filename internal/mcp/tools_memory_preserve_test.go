package mcp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/files"
)

// memory_write updates in place, and an update must not quietly discard the
// curation the tool has no arguments for. Tags, the favorite star, and the
// unknown frontmatter keys Extra round-trips are all invisible to the write
// call, so a body correction used to erase them: the struct it renders is the
// whole file, with no merge against what is on disk.
//
// Everything is asserted against the markdown file, not the index row: files are
// the source of truth for durable knowledge, and Extra is never mirrored to the
// index at all.
func TestMemoryWrite_PreservesCurationOnUpdate(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)

	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "preserve-sess"})

	// Seed through the files layer: tags, favorite, and unknown keys cannot be
	// expressed through the tool surface, which is the whole point of the bug.
	id, err := core.NewID()
	require.NoError(t, err)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	seeded := core.Memory{
		ID: id, Kind: core.KindGotcha, Name: "curated", Description: "seeded description",
		Project: "demo", Body: "the original body", Tags: []string{"created-by:agent", "topic:dfu"},
		Favorite: true, Extra: map[string]any{"obsidian_plugin": "dataview", "owner_pinned": true},
		Created: created, Updated: created, ValidFrom: created,
	}
	_, err = mgr.WriteMemory(ctx, seeded)
	require.NoError(t, err)

	// A plain correction: new body, new description, no mention of curation.
	out := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "curated", "kind": "gotcha", "description": "corrected description",
		"body": "the corrected body",
	})
	require.Equal(t, true, out["updated"], "should update in place, not create")
	require.Equal(t, id, out["id"], "identity must survive an update")

	onDisk, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "curated"))
	require.NoError(t, err)

	// The write did its job.
	require.Equal(t, "the corrected body", strings.TrimSpace(onDisk.Body))
	require.Equal(t, "corrected description", onDisk.Description)

	// ...without collateral damage.
	require.Equal(t, []string{"created-by:agent", "topic:dfu"}, onDisk.Tags, "tags must survive a body correction")
	require.True(t, onDisk.Favorite, "a starred memory must stay starred: the star drives briefing pinning and the recall boost")
	require.Equal(t, "dataview", onDisk.Extra["obsidian_plugin"], "unknown frontmatter keys must round-trip")
	require.Equal(t, true, onDisk.Extra["owner_pinned"])

	// Identity and creation provenance are unchanged (the pre-existing contract).
	require.Equal(t, id, onDisk.ID)
	require.True(t, created.Equal(onDisk.Created), "created: want %s, got %s", created, onDisk.Created)

	// The star also still reads back through the tool surface, which is what
	// retrieval consults.
	read := callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "curated"})
	require.Equal(t, true, read["favorite"])
	require.ElementsMatch(t, []any{"created-by:agent", "topic:dfu"}, read["tags"])
}

// The tags argument is an override, not a default. Sending it replaces the set;
// omitting it leaves the existing tags alone; an empty list is read as absent
// (validateMiddleware drops it), so it is not a clear.
func TestMemoryWrite_TagsArgument(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)

	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "tags-sess"})
	tagsOf := func(name string) []string {
		t.Helper()
		onDisk, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", name))
		require.NoError(t, err)
		return onDisk.Tags
	}

	// Create with tags.
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "tagged", "kind": "gotcha", "description": "d", "body": "b",
		"tags": []any{"alpha", "beta"},
	})
	require.Equal(t, []string{"alpha", "beta"}, tagsOf("tagged"))

	// Update WITHOUT tags: untouched (the preservation path).
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "tagged", "kind": "gotcha", "description": "d2", "body": "b2",
	})
	require.Equal(t, []string{"alpha", "beta"}, tagsOf("tagged"), "omitting tags must not clear them")

	// Update WITH tags: replaces the whole set, dropping "beta".
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "tagged", "kind": "gotcha", "description": "d3", "body": "b3",
		"tags": []any{"alpha", "gamma"},
	})
	require.Equal(t, []string{"alpha", "gamma"}, tagsOf("tagged"), "tags replace all, they do not merge")

	// An empty list means absent, not clear -- the documented contract, shared
	// with notes_update.
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "tagged", "kind": "gotcha", "description": "d4", "body": "b4",
		"tags": []any{},
	})
	require.Equal(t, []string{"alpha", "gamma"}, tagsOf("tagged"), "an empty list is absent, not a clear")

	// The comma-separated form the description advertises is coerced too.
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "tagged", "kind": "gotcha", "description": "d5", "body": "b5",
		"tags": "delta, epsilon",
	})
	require.Equal(t, []string{"delta", "epsilon"}, tagsOf("tagged"))

	// Re-tagging leaves the rest of the curation alone: the star survives.
	callJSON(t, ctx, cli, "favorite_set", map[string]any{"kind": "memory", "id": "tagged", "favorite": true})
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "tagged", "kind": "gotcha", "description": "d6", "body": "b6",
		"tags": []any{"zeta"},
	})
	onDisk, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "tagged"))
	require.NoError(t, err)
	require.Equal(t, []string{"zeta"}, onDisk.Tags)
	require.True(t, onDisk.Favorite, "re-tagging must not unstar")
}

// A brand-new memory starts uncurated: preservation must not invent tags or a
// star for a name that did not exist.
func TestMemoryWrite_NewMemoryStartsUncurated(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, _ := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)

	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "fresh-sess"})
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "fresh", "kind": "reference", "description": "d", "body": "b",
	})

	onDisk, err := mgr.Store().ReadMemory(files.MemoryRelPath("demo", "fresh"))
	require.NoError(t, err)
	require.Empty(t, onDisk.Tags)
	require.False(t, onDisk.Favorite)
	require.Empty(t, onDisk.Extra)
}
