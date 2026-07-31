package mcp_test

import (
	"context"
	"database/sql"
	"net/http"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	mcpserver "github.com/0spoon/seamless/internal/mcp"
	"github.com/0spoon/seamless/internal/store"
)

// toolCallEvents returns the recorded tool.call events, newest first.
func toolCallEvents(t *testing.T, db *sql.DB) []core.Event {
	t.Helper()
	rec := events.NewRecorder(db)
	evs, err := rec.ByKinds(context.Background(), []core.EventKind{core.EventToolCall}, "", "", 200)
	require.NoError(t, err)
	return evs
}

// findToolCall returns the newest tool.call event for the named tool.
func findToolCall(t *testing.T, db *sql.DB, tool string) core.Event {
	t.Helper()
	for _, e := range toolCallEvents(t, db) {
		if e.Payload["tool"] == tool {
			return e
		}
	}
	t.Fatalf("no tool.call event for %q", tool)
	return core.Event{}
}

func TestLogMiddleware_RecordsArgsResultAndAttribution(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	// session_start binds the connection to project "demo" via the cwd map.
	start := callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo/sub", "source": "startup"})
	sessID, _ := start["session_id"].(string)
	require.NotEmpty(t, sessID)

	// A durable write that inherits the bound scope.
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "mw-logged", "kind": "gotcha", "description": "logged by the middleware",
		"body": "body text",
	})

	// session_start's own event carries the session it just bound (attribution read
	// after next()).
	ss := findToolCall(t, db, "session_start")
	require.Equal(t, sessID, ss.SessionID, "session_start event attributed to the session it created")
	require.Equal(t, "demo", ss.ProjectSlug)
	require.NotNil(t, ss.Payload["args"])
	require.NotNil(t, ss.Payload["result"])
	_, hasDur := ss.Payload["duration_ms"]
	require.True(t, hasDur)

	mw := findToolCall(t, db, "memory_write")
	require.Equal(t, sessID, mw.SessionID)
	require.Equal(t, "demo", mw.ProjectSlug)
	args, ok := mw.Payload["args"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "mw-logged", args["name"])
	require.NotEmpty(t, mw.Payload["result"])
	require.Nil(t, mw.Payload["is_error"])
}

func TestLogMiddleware_RecordsErrorResult(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	// memory_read of a missing name is an error result (not a Go error).
	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "memory_read", Arguments: map[string]any{"name": "does-not-exist"},
	}})
	require.NoError(t, err)
	require.True(t, res.IsError)

	e := findToolCall(t, db, "memory_read")
	require.Equal(t, true, e.Payload["is_error"])
	require.NotEmpty(t, e.Payload["error"])
}

func TestLogMiddleware_Truncation(t *testing.T) {
	ctx := context.Background()
	url, db := newServerCfg(t, func(c *mcpserver.Config) { c.ToolEventMaxChars = 16 })
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	long := "abcdefghijklmnopqrstuvwxyz0123456789" // 36 runes, over the 16 cap
	callJSON(t, ctx, cli, "notes_create", map[string]any{
		"title": "trunc-note", "body": long,
	})

	e := findToolCall(t, db, "notes_create")
	args, ok := e.Payload["args"].(map[string]any)
	require.True(t, ok)
	body, _ := args["body"].(string)
	require.Contains(t, body, "truncated", "long arg value should be truncated")
	require.Less(t, len([]rune(body)), len([]rune(long)))
}

// TestLogMiddleware_SessionEndExplicitIDAttribution is the regression test for
// the session_end misattribution: an unbound connection ends one of two
// same-project ambient sessions by explicit session_id. The ended session is
// then invisible to the active-only ambient fallback, so pre-fix the tool.call
// (and its heartbeat) landed on the surviving sibling. The handler's stashed
// target must win instead.
func TestLogMiddleware_SessionEndExplicitIDAttribution(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)

	target := seedAmbient(t, ctx, db, "cx/target00", "demo")
	sibling := seedAmbient(t, ctx, db, "cc/sibling0", "demo")
	before, ok, err := store.SessionByID(ctx, db, sibling)
	require.NoError(t, err)
	require.True(t, ok)

	cli := dialClient(t, ctx, url, testKey)
	end := callJSON(t, ctx, cli, "session_end", map[string]any{
		"findings": "done", "session_id": target,
	})
	require.Equal(t, target, end["session_id"])

	e := findToolCall(t, db, "session_end")
	require.Equal(t, target, e.SessionID,
		"tool.call must be attributed to the ended session, not the surviving sibling")
	require.Equal(t, "demo", e.ProjectSlug)

	after, ok, err := store.SessionByID(ctx, db, sibling)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, core.SessionActive, after.Status)
	require.True(t, after.UpdatedAt.Equal(before.UpdatedAt),
		"ending one agent's session must not heartbeat the sibling's updated_at")
}

// TestLogMiddleware_SessionUpdateExplicitNameAttribution pins the same contract
// for session_update naming its target: with ambient sessions spanning two
// projects the fallback refuses, so pre-fix the tool.call landed unattributed
// even though the call named exactly the session it operated on.
func TestLogMiddleware_SessionUpdateExplicitNameAttribution(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)

	target := seedAmbient(t, ctx, db, "cx/target00", "demo")
	seedAmbient(t, ctx, db, "cc/elsewher", "other")

	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_update", map[string]any{
		"findings": "progress", "session": "cx/target00",
	})

	e := findToolCall(t, db, "session_update")
	require.Equal(t, target, e.SessionID,
		"tool.call must carry the named target, not fall to the ambiguous ambient guess")
	require.Equal(t, "demo", e.ProjectSlug)
}

// TestLogMiddleware_BoundSessionEndAttribution covers the bound variant of the
// same bug: session_end evicts the connection's binding inside the handler, so
// the post-handler attribution read found no binding and fell through to the
// ambient fallback -- a sibling ambient in the same project.
func TestLogMiddleware_BoundSessionEndAttribution(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)

	cli := dialClient(t, ctx, url, testKey)
	start := callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})
	bound, _ := start["session_id"].(string)
	require.NotEmpty(t, bound)

	sibling := seedAmbient(t, ctx, db, "cc/sibling0", "demo")
	before, ok, err := store.SessionByID(ctx, db, sibling)
	require.NoError(t, err)
	require.True(t, ok)

	callJSON(t, ctx, cli, "session_end", map[string]any{"findings": "done"})

	e := findToolCall(t, db, "session_end")
	require.Equal(t, bound, e.SessionID,
		"a bound session_end must be attributed to the session it completed, binding eviction notwithstanding")
	require.Equal(t, "demo", e.ProjectSlug)

	after, ok, err := store.SessionByID(ctx, db, sibling)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, after.UpdatedAt.Equal(before.UpdatedAt),
		"the sibling ambient must not receive the ended session's heartbeat")
}

func TestLogMiddleware_UnauthorizedLogsNothing(t *testing.T) {
	url, db := newServer(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"project_list","arguments":{}}}`
	status, _, _ := rawMCPRequest(t, url, http.MethodPost, "wrong-key", body)
	require.Equal(t, http.StatusUnauthorized, status)

	require.Empty(t, toolCallEvents(t, db), "unauthorized calls must not be logged")
}
