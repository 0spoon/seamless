package mcp_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	mcpserver "github.com/0spoon/seamless/internal/mcp"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

const testKey = "test-bearer-key"

// newServer builds a full MCP server over a temp store and returns its base URL.
func newServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, store.SetSetting(context.Background(), db,
		store.SettingRepoProjectMap, `{"/work/demo":"demo"}`))

	mgr, err := files.NewManager(dir, db, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })

	ret := retrieve.New(db, nil, config.Budgets{MaxBriefingTokens: 1500, RecallBudgetTokens: 1000}, nil)
	srv := mcpserver.New(mcpserver.Config{
		DB: db, Files: mgr, Retrieve: ret, Events: events.NewRecorder(db), APIKey: testKey,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

// dialClient starts and initializes an MCP client against url with the given key.
func dialClient(t *testing.T, ctx context.Context, url, key string) *mcpclient.Client {
	t.Helper()
	cli, err := mcpclient.NewStreamableHttpClient(url,
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + key}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	require.NoError(t, cli.Start(ctx))

	var initReq mcp.InitializeRequest
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "0"}
	_, err = cli.Initialize(ctx, initReq)
	require.NoError(t, err)
	return cli
}

func callJSON(t *testing.T, ctx context.Context, cli *mcpclient.Client, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
	require.NoError(t, err)
	require.False(t, res.IsError, "tool %s errored: %s", name, resultText(t, res))
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &out))
	return out
}

// projectSlugs extracts the slugs from a project_list result.
func projectSlugs(pl map[string]any) []string {
	ps, _ := pl["projects"].([]any)
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if m, ok := p.(map[string]any); ok {
			if slug, ok := m["slug"].(string); ok {
				out = append(out, slug)
			}
		}
	}
	return out
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, res)
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	t.Fatalf("no text content in result: %+v", res)
	return ""
}

func TestMCPLoopWithBinding(t *testing.T) {
	ctx := context.Background()
	url := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	// session_start binds the connection to project "demo" via the cwd map.
	start := callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo/sub", "source": "startup"})
	require.Equal(t, "demo", start["project"])
	require.NotEmpty(t, start["session_id"])

	// memory_write with NO project inherits "demo" from the binding.
	w := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "chroma-boot-race", "kind": "gotcha",
		"description": "chroma answers health checks before it can serve",
		"body":        "Add a readiness gate.\n",
	})
	require.Equal(t, false, w["updated"])
	memID := w["id"].(string)
	require.NotEmpty(t, memID)

	// memory_read with NO project resolves via the binding and returns the body.
	r := callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "chroma-boot-race"})
	require.Equal(t, "demo", r["project"])
	require.Equal(t, memID, r["id"])
	require.Contains(t, r["body"], "readiness gate")

	// Writing the same name again updates in place: the id is stable.
	w2 := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "chroma-boot-race", "kind": "gotcha",
		"description": "chroma readiness race, revised",
		"body":        "Add a readiness gate and a healthcheck.\n",
	})
	require.Equal(t, true, w2["updated"])
	require.Equal(t, memID, w2["id"], "same name must reuse the same ULID")

	// recall (FTS-only; no embedder) finds it, inheriting the bound project.
	rec := callJSON(t, ctx, cli, "recall", map[string]any{"query": "chroma health check readiness"})
	hits := rec["hits"].([]any)
	require.NotEmpty(t, hits)
	require.Equal(t, "chroma-boot-race", hits[0].(map[string]any)["name"])

	// notes roundtrip.
	nc := callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "Boot race writeup", "body": "The fix was a readiness gate.",
	})
	noteID := nc["id"].(string)
	nr := callJSON(t, ctx, cli, "notes_read", map[string]any{"id": noteID})
	require.Contains(t, nr["body"], "readiness gate")

	// projects: session_start auto-registered "demo" from the cwd map, so it
	// already appears in project_list without an explicit project_create.
	pl := callJSON(t, ctx, cli, "project_list", nil)
	require.Contains(t, projectSlugs(pl), "demo")

	// A distinct project_create adds another; both are then listed.
	pc := callJSON(t, ctx, cli, "project_create", map[string]any{"name": "Other Project", "slug": "other"})
	require.Equal(t, "other", pc["slug"])
	pl = callJSON(t, ctx, cli, "project_list", nil)
	require.Subset(t, projectSlugs(pl), []string{"demo", "other"})

	// session_end persists findings.
	end := callJSON(t, ctx, cli, "session_end", map[string]any{"findings": "readiness gate fixes the boot race"})
	require.Equal(t, "completed", end["status"])
}

func TestMCPAuthRejectsBadKey(t *testing.T) {
	ctx := context.Background()
	url := newServer(t)
	cli := dialClient(t, ctx, url, "wrong-key")

	// Initialize is open, but a tool call with a bad key is rejected.
	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "project_list"}})
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "unauthorized")
}
