package mcp_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	mcpserver "github.com/0spoon/seamless/internal/mcp"
)

// firstReuseEvents counts the memory.first_reuse marks for one memory id.
func firstReuseEvents(t *testing.T, db *sql.DB, itemID string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM events WHERE kind = ? AND item_id = ?`,
		string(core.EventMemoryFirstReuse), itemID).Scan(&n))
	return n
}

// The first-reuse moment: latched exactly once per memory ever, only for a
// reader that is not the writer, and only while the momentum feature is on.
func TestMemoryRead_FirstReuseMark(t *testing.T) {
	ctx := context.Background()
	url, db := newServerCfg(t, func(c *mcpserver.Config) {
		c.Features = config.Features{Momentum: true}
	})

	writer := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, writer, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})
	w := callJSON(t, ctx, writer, "memory_write", map[string]any{
		"name": "payoff-mem", "kind": "gotcha", "description": "d", "body": "the lesson",
	})
	memID, _ := w["id"].(string)
	require.NotEmpty(t, memID)

	// The writer re-reading its own memory is not reuse.
	callJSON(t, ctx, writer, "memory_read", map[string]any{"name": "payoff-mem"})
	require.Zero(t, firstReuseEvents(t, db, memID))

	// A different session reading it is -- once, ever.
	reader := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, reader, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup", "name": "sess/reader-one"})
	callJSON(t, ctx, reader, "memory_read", map[string]any{"name": "payoff-mem"})
	require.Equal(t, 1, firstReuseEvents(t, db, memID))

	callJSON(t, ctx, reader, "memory_read", map[string]any{"name": "payoff-mem"})
	third := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, third, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup", "name": "sess/reader-two"})
	callJSON(t, ctx, third, "memory_read", map[string]any{"name": "payoff-mem"})
	require.Equal(t, 1, firstReuseEvents(t, db, memID), "the moment is minted once, ever")

	// The payload carries what the ledger line and the toast need.
	var payload string
	require.NoError(t, db.QueryRow(
		`SELECT payload FROM events WHERE kind = ? AND item_id = ?`,
		string(core.EventMemoryFirstReuse), memID).Scan(&payload))
	require.Contains(t, payload, `"name":"payoff-mem"`)
	require.Contains(t, payload, `"kind":"gotcha"`)
	require.Contains(t, payload, `"reader":"sess/reader-one"`)
}

// With the feature off (the shipped default) detection never runs: a disabled
// install records nothing and behaves exactly as before.
func TestMemoryRead_FirstReuseOffByDefault(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)

	writer := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, writer, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})
	w := callJSON(t, ctx, writer, "memory_write", map[string]any{
		"name": "quiet-mem", "kind": "gotcha", "description": "d", "body": "the lesson",
	})
	memID, _ := w["id"].(string)

	reader := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, reader, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup", "name": "sess/off-reader"})
	callJSON(t, ctx, reader, "memory_read", map[string]any{"name": "quiet-mem"})
	require.Zero(t, firstReuseEvents(t, db, memID))
}
