package mcp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/store"
)

// callErr calls a tool and returns (isError, text) without failing the test, for
// asserting on rejections.
func callErr(t *testing.T, ctx context.Context, cli interface {
	CallTool(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
}, name string, args map[string]any) (bool, string) {
	t.Helper()
	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
	require.NoError(t, err)
	return res.IsError, resultText(t, res)
}

// countSlug returns how many times slug appears in a project_list result, so a
// repeated registration can be asserted to be idempotent rather than additive.
func countSlug(slugs []string, slug string) int {
	n := 0
	for _, s := range slugs {
		if s == slug {
			n++
		}
	}
	return n
}

// TestBodyContentTextAliases verifies the item-text param is accepted under any
// of body/content/text, so an agent primed on one tool's name succeeds on the
// append tools (the top field-name mistake in the logs).
func TestBodyContentTextAliases(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	// memory_write accepts "content" in place of "body".
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "aliased", "kind": "reference", "description": "d",
		"content": "written via content alias", "project": "global",
	})
	r := callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "aliased", "project": "global"})
	require.Contains(t, r["body"], "written via content alias")

	// memory_append accepts "body" in place of "content".
	callJSON(t, ctx, cli, "memory_append", map[string]any{
		"name": "aliased", "body": "appended via body alias", "project": "global",
	})
	r = callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "aliased", "project": "global"})
	require.Contains(t, r["body"], "appended via body alias")

	// notes_append accepts "content" in place of its historical "text".
	nc := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "a note", "body": "seed", "project": "global",
	})
	noteID, _ := nc["id"].(string)
	require.NotEmpty(t, noteID)
	isErr, txt := callErr(t, ctx, cli, "notes_append", map[string]any{"id": noteID, "content": "line via content"})
	require.False(t, isErr, txt)
}

// TestGlobalNamespaceExplicit verifies project=global targets the global scope
// deliberately, and is readable back as a global memory.
func TestGlobalNamespaceExplicit(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	w := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "a-global-fact", "kind": "reference", "description": "d",
		"body": "b", "project": "global",
	})
	require.Equal(t, "", w["project"], "project=global normalizes to the empty global scope")

	_, found, err := store.MemoryByName(ctx, db, "", "a-global-fact")
	require.NoError(t, err)
	require.True(t, found, "the memory lands in the global scope")
}

// TestAmbiguousScopeRejected verifies a durable create with no bound session, no
// ambient session, and no explicit project is rejected rather than silently
// landing in global.
func TestAmbiguousScopeRejected(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	isErr, txt := callErr(t, ctx, cli, "memory_write", map[string]any{
		"name": "orphan", "kind": "reference", "description": "d", "body": "b",
	})
	require.True(t, isErr, "unscoped create must be rejected")
	require.Contains(t, txt, "ambiguous scope")

	// The same create with an explicit scope succeeds.
	isErr, txt = callErr(t, ctx, cli, "memory_write", map[string]any{
		"name": "orphan", "kind": "reference", "description": "d", "body": "b", "project": "global",
	})
	require.False(t, isErr, txt)
}

// TestWriteRegistersNamedProject is the regression for the orphan-scope gap that
// taught an agent to distrust naming a new project. A durable create into a slug
// no session, repo map, or import had registered used to write the file and its
// index row under the slug while leaving the projects table untouched -- so the
// project existed for the write but was absent from project_list and the console
// until some unrelated path backfilled the row. The write now registers the slug
// it is given, which is what makes "an unknown slug creates that project" a
// promise the tools can keep.
func TestWriteRegistersNamedProject(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	require.NotContains(t, projectSlugs(callJSON(t, ctx, cli, "project_list", nil)), "neuro-lab",
		"precondition: the slug is unknown before the write")

	w := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "first-fact", "kind": "reference", "description": "d",
		"body": "b", "project": "neuro-lab",
	})
	require.Equal(t, "neuro-lab", w["project"], "the write lands in the named project")

	require.Contains(t, projectSlugs(callJSON(t, ctx, cli, "project_list", nil)), "neuro-lab",
		"a write into an unknown slug must register the project, not orphan the scope")

	// Every durable create shares the guard, so the note lands in a registered
	// project too -- and re-registering an existing slug is a no-op, not a conflict.
	callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "a note", "body": "b", "project": "neuro-lab",
	})
	callJSON(t, ctx, cli, "tasks_add", map[string]any{"title": "a task", "project": "field-rig"})

	slugs := projectSlugs(callJSON(t, ctx, cli, "project_list", nil))
	require.Contains(t, slugs, "field-rig", "tasks_add resolves through the same guard")
	require.Equal(t, 1, countSlug(slugs, "neuro-lab"), "an existing slug is registered once, not duplicated")

	// The registration is a real row, not just a project_list artifact.
	p, found, err := store.ProjectBySlug(ctx, db, "neuro-lab")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "neuro-lab", p.Name, "a slug-only registration names the project after its slug")
}

// TestGlobalWriteRegistersNothing verifies the registration is scoped to real
// projects: project=global still normalizes to the empty scope and must not mint
// a project row named for the reserved token.
func TestGlobalWriteRegistersNothing(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "a-global-fact", "kind": "reference", "description": "d",
		"body": "b", "project": "global",
	})
	slugs := projectSlugs(callJSON(t, ctx, cli, "project_list", nil))
	require.NotContains(t, slugs, "global")
	require.NotContains(t, slugs, "")
	require.Empty(t, slugs, "a global write registers no project at all")
}

// TestWriteRejectsReservedAllToken verifies the widening token cannot be written
// into. project_create has always refused the slug -- gardener_request reads "all"
// as every project, so a project named for it is unaddressable there -- but shape
// validation let a write pass it through as an ordinary slug. Harmless only while
// nothing acted on it; now that a write registers what it is handed, it would mint
// exactly the row project_create forbids.
func TestWriteRejectsReservedAllToken(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	isErr, txt := callErr(t, ctx, cli, "memory_write", map[string]any{
		"name": "x", "kind": "reference", "description": "d", "body": "b", "project": "all",
	})
	require.True(t, isErr, "the widening token is not a writable project")
	require.Contains(t, txt, "reserved")
	require.Empty(t, projectSlugs(callJSON(t, ctx, cli, "project_list", nil)),
		"the rejected write registers nothing")
}

// TestScopeErrorGuidesTheChoice pins the guidance in the scope errors, which is
// the half of this fix that reaches the agent at the moment it must choose. The
// original mistake was not a missing guard -- the guard fired exactly as designed.
// It was that the error named two options and said nothing about how to pick
// between them, so an agent that could not tell whether a new slug would error
// took project=global as the conservative choice. It is the opposite: global is
// the only scope that reaches every project's briefing.
func TestScopeErrorGuidesTheChoice(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)

	seedAmbient(t, ctx, db, "cc/ambdemo0", "demo")
	seedAmbient(t, ctx, db, "cc/ambother", "other")
	_, err := store.EnsureProject(ctx, db, "demo", "demo")
	require.NoError(t, err)

	cli := dialClient(t, ctx, url, testKey)
	isErr, txt := callErr(t, ctx, cli, "memory_write", map[string]any{
		"name": "x", "kind": "reference", "description": "d", "body": "b",
	})
	require.True(t, isErr)
	require.Contains(t, txt, "CREATES that project",
		"the error must settle the question the agent cannot answer: a new slug works")
	require.Contains(t, txt, "not a neutral fallback",
		"the error must not let global keep looking like the safe default")
	require.Contains(t, txt, "known projects: demo",
		"the error names the slugs that exist, so a near-duplicate is not coined")
}

// TestSessionEndAcceptsLongFindings verifies the old 1500-char cap is gone:
// long findings are stored in full, not rejected.
func TestSessionEndAcceptsLongFindings(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	start := callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})
	sessID, _ := start["session_id"].(string)
	require.NotEmpty(t, sessID)

	long := strings.Repeat("x", 5000)
	end := callJSON(t, ctx, cli, "session_end", map[string]any{"findings": long})
	require.Equal(t, "completed", end["status"])

	sess, ok, err := store.SessionByID(ctx, db, sessID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 5000, len(sess.Findings), "long findings are stored in full")
}

// TestProjectCreateRejectsReservedSlug verifies the global namespace token
// cannot be claimed as a real project slug.
func TestProjectCreateRejectsReservedSlug(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	for _, slug := range []string{"global", "_global"} {
		isErr, txt := callErr(t, ctx, cli, "project_create", map[string]any{"name": "X", "slug": slug})
		require.True(t, isErr, "slug %q must be rejected", slug)
		require.Contains(t, txt, "reserved")
	}
}
