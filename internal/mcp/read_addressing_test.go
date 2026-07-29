package mcp_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// memory_read accepts id as an alternative address: agents legitimately hold
// memory ULIDs from events, recall results, and gardener proposal payloads,
// and were sending them to memory_read (the gardener's tool-error evidence).
func TestMemoryRead_ByID(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	w := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "id-addressed", "kind": "reference", "description": "d",
		"body": "read me by id", "project": "global",
	})
	id, _ := w["id"].(string)
	require.NotEmpty(t, id)

	r := callJSON(t, ctx, cli, "memory_read", map[string]any{"id": id})
	require.Equal(t, "id-addressed", r["name"])
	require.Contains(t, r["body"], "read me by id")

	// A missing id is a loud, specific error.
	isErr, txt := callErr(t, ctx, cli, "memory_read", map[string]any{"id": "01AAAAAAAAAAAAAAAAAAAAAAAA"})
	require.True(t, isErr)
	require.Contains(t, txt, "no memory with id")

	// name and id together are ambiguous, not a precedence guess.
	isErr, txt = callErr(t, ctx, cli, "memory_read", map[string]any{"id": id, "name": "id-addressed"})
	require.True(t, isErr)
	require.Contains(t, txt, "exactly one of name or id")

	// Neither name nor id names both addresses in the rejection.
	isErr, txt = callErr(t, ctx, cli, "memory_read", map[string]any{})
	require.True(t, isErr)
	require.Contains(t, txt, "name or id is required")
}

// query= on memory_read is an agent trying to search; the rejection points at
// recall instead of leaving it to guess which parameter to respell.
func TestMemoryRead_QueryRedirectsToRecall(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	isErr, txt := callErr(t, ctx, cli, "memory_read", map[string]any{"query": "anything"})
	require.True(t, isErr)
	require.Contains(t, txt, `unknown parameter "query"`)
	require.Contains(t, txt, "use the recall tool")
}

// notes_read accepts slug (with a name alias): briefings, plan compositions,
// and notes_create responses all name notes by slug, so slug is the vocabulary
// agents actually hold -- id-only was the asymmetry behind the recurring
// unknown-parameter errors.
func TestNotesRead_BySlug(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	nc := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Slug Addressed Note", "body": "note body here", "project": "global",
	})
	slug, _ := nc["slug"].(string)
	require.NotEmpty(t, slug)

	r := callJSON(t, ctx, cli, "notes_read", map[string]any{"slug": slug, "project": "global"})
	require.Contains(t, r["body"], "note body here")

	// The name alias lands on the slug parameter.
	r = callJSON(t, ctx, cli, "notes_read", map[string]any{"name": slug, "project": "global"})
	require.Contains(t, r["body"], "note body here")

	// A miss names the scope searched and the other address forms.
	isErr, txt := callErr(t, ctx, cli, "notes_read", map[string]any{"slug": "no-such-note", "project": "global"})
	require.True(t, isErr)
	require.Contains(t, txt, "no note named")
	require.Contains(t, txt, "recall")

	// id and slug together are ambiguous, not a precedence guess.
	id, _ := nc["id"].(string)
	require.NotEmpty(t, id)
	isErr, txt = callErr(t, ctx, cli, "notes_read", map[string]any{"id": id, "slug": slug})
	require.True(t, isErr)
	require.Contains(t, txt, "exactly one of id or slug")

	// Neither id nor slug names both addresses in the rejection.
	isErr, txt = callErr(t, ctx, cli, "notes_read", map[string]any{})
	require.True(t, isErr)
	require.Contains(t, txt, "id or slug is required")
}
