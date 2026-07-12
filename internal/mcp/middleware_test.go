package mcp_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	mcpserver "github.com/0spoon/seamless/internal/mcp"
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

func TestLogMiddleware_UnauthorizedLogsNothing(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	// A client with the wrong key: auth rejects before the logger runs.
	cli := dialClient(t, ctx, url, "wrong-key")
	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "project_list"}})
	require.NoError(t, err)
	require.True(t, res.IsError)

	require.Empty(t, toolCallEvents(t, db), "unauthorized calls must not be logged")
}
