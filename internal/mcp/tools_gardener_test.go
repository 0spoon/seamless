package mcp_test

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/store"
)

func TestGardenerProposalsAndApply(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	// Create a memory through the tool so it exists on disk + index; an archive
	// apply then retires it.
	wrote := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "stale-thing", "kind": "gotcha",
		"description": "an old memory", "body": "body text", "project": "global",
	})
	memID, _ := wrote["id"].(string)
	require.NotEmpty(t, memID)

	// Seed archive + digest proposals directly.
	arch, err := store.CreateProposal(ctx, db, store.ProposalArchive, map[string]any{
		"key": "archive:" + memID, "id": memID, "name": "stale-thing",
	})
	require.NoError(t, err)
	dig, err := store.CreateProposal(ctx, db, store.ProposalDigest, map[string]any{
		"key": "digest:demo:2026-07", "project": "",
		"title": "Session digest -- 2026-07", "body": "- did work",
	})
	require.NoError(t, err)

	// gardener_proposals lists both; the kind filter narrows.
	all := callJSON(t, ctx, cli, "gardener_proposals", nil)
	require.Equal(t, float64(2), all["count"])
	archives := callJSON(t, ctx, cli, "gardener_proposals", map[string]any{"kind": "archive"})
	require.Equal(t, float64(1), archives["count"])

	// Apply the archive: the memory is retired (leaves the active index).
	applied := callJSON(t, ctx, cli, "gardener_apply", map[string]any{"id": arch.ID})
	require.Equal(t, "applied", applied["status"])
	require.Equal(t, "archive", applied["kind"])
	_, found, err := store.MemoryByName(ctx, db, "", "stale-thing")
	require.NoError(t, err)
	require.False(t, found, "archived memory must leave the active index")

	// Dismiss the digest: no side effect, just resolved.
	dismissed := callJSON(t, ctx, cli, "gardener_apply", map[string]any{"id": dig.ID, "action": "dismiss"})
	require.Equal(t, "dismissed", dismissed["status"])

	// Nothing pending now.
	none := callJSON(t, ctx, cli, "gardener_proposals", nil)
	require.Equal(t, float64(0), none["count"])

	// Applying an already-resolved proposal is a tool error, not a transport error.
	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "gardener_apply", Arguments: map[string]any{"id": arch.ID},
	}})
	require.NoError(t, err)
	require.True(t, res.IsError)
}

// TestGardenerRequest_NoChatIsToolError exercises the natural-language request
// tool on a server whose gardener has no chat client: it must surface a tool
// error rather than fabricate proposals. (A success-path test needs a
// chat-enabled server helper, which this fixture does not build.)
func TestGardenerRequest_NoChatIsToolError(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "gardener_request", Arguments: map[string]any{"request": "merge the duplicate memories"},
	}})
	require.NoError(t, err, "transport succeeds")
	require.True(t, res.IsError, "a gardener with no chat client returns a tool error")
}

// TestGardenerRequestScopeGuards covers the last tool argument that reached a
// service without passing the scope guards. Each rejection below was a SUCCESS
// before this: an unresolvable scope matched no rows, and "no active memories in
// scope" is indistinguishable from a genuinely empty project.
//
// The guards run before the interpretation, so the fixture's chat-less gardener
// is enough to exercise them: a rejection here means the scope was refused, and
// reaching the no-chat error means it was accepted.
func TestGardenerRequestScopeGuards(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	// A well-formed slug that is not a project. validate.Name only checks slug
	// SHAPE, so "typoed" passes every shared guard -- which is why the existence
	// check lives in this handler, and why the acceptance criterion needs it.
	isErr, txt := callErr(t, ctx, cli, "gardener_request", map[string]any{
		"request": "merge the duplicates", "project": "typoed",
	})
	require.True(t, isErr, "an unknown project must not report an empty scope as success")
	require.Contains(t, txt, `unknown project "typoed"`)

	// The traversal guard reaches this tool now too.
	isErr, txt = callErr(t, ctx, cli, "gardener_request", map[string]any{
		"request": "merge the duplicates", "project": "../notes/_global",
	})
	require.True(t, isErr, "an unsafe project slug must be rejected")
	require.Contains(t, txt, "invalid project")

	// Every accepted scope gets PAST the guards and fails on the missing chat
	// client instead -- which is what proves it was accepted rather than refused.
	//
	// "_global" is the sharpest of these: it is not a registered project, so it
	// can only clear the existence check if normalizeProject mapped it to the
	// global scope first. Unnormalized, it used to reach ActiveMemories("_global"),
	// match nothing, and report success. "demo" clears it because session_start
	// registers the project it resolves from the cwd (RegisterProjectForCWD
	// backfills EnsureProject), which is what keeps this check from ever
	// rejecting a real, session-reachable project.
	for _, project := range []string{"global", "_global", "all", "demo"} {
		isErr, txt := callErr(t, ctx, cli, "gardener_request", map[string]any{
			"request": "merge the duplicates", "project": project,
		})
		require.True(t, isErr, "the chat-less fixture always errors")
		require.NotContains(t, txt, "unknown project", "project %q must be accepted as a scope", project)
		require.NotContains(t, txt, "invalid project", "project %q must be accepted as a scope", project)
	}
}

// TestProjectCreateRejectsAllToken pins the reservation the widening token needs:
// without it a project could take the name "all" and then be permanently
// unreachable through gardener_request, which reads the token before it resolves
// anything as a slug.
func TestProjectCreateRejectsAllToken(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	isErr, txt := callErr(t, ctx, cli, "project_create", map[string]any{"name": "All", "slug": "all"})
	require.True(t, isErr, `slug "all" must be reserved`)
	require.Contains(t, txt, "reserved")
}

// TestGardenerSplit_NoChatIsToolError exercises the project-split tool on a
// server whose gardener has no chat client: it must surface a tool error rather
// than create any projects or proposals.
func TestGardenerSplit_NoChatIsToolError(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "gardener_split", Arguments: map[string]any{"source": "arctop-app", "instruction": "split into ios and android"},
	}})
	require.NoError(t, err, "transport succeeds")
	require.True(t, res.IsError, "a gardener with no chat client returns a tool error")
}
