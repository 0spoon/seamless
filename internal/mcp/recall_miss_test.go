package mcp_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// missEvents reads the recall.miss rows straight off the event log.
func missEvents(t *testing.T, db *sql.DB) []map[string]any {
	t.Helper()
	rows, err := db.Query(`SELECT project_slug, session_id, payload FROM events WHERE kind = ?`,
		string(core.EventRecallMiss))
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []map[string]any
	for rows.Next() {
		var project, session, payload string
		require.NoError(t, rows.Scan(&project, &session, &payload))
		var p map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &p))
		p["_project"] = project
		p["_session"] = session
		out = append(out, p)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestRecall_ZeroHitRecordsMiss(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	start := callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})
	sessID := start["session_id"].(string)

	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "chroma-boot-race", "kind": "gotcha",
		"description": "chroma readiness boot race",
		"body":        "Add a readiness gate.\n",
	})

	// A query nothing matches: the zero-hit recall must leave a miss event
	// carrying the query and the session/project attribution.
	rec := callJSON(t, ctx, cli, "recall", map[string]any{"query": "quantum flux capacitor sizing", "limit": 3})
	require.Empty(t, rec["hits"])

	misses := missEvents(t, db)
	require.Len(t, misses, 1)
	require.Equal(t, "quantum flux capacitor sizing", misses[0]["query"])
	require.Equal(t, "recall", misses[0]["source"])
	require.Equal(t, float64(3), misses[0]["limit"])
	require.Equal(t, "demo", misses[0]["_project"])
	require.Equal(t, sessID, misses[0]["_session"])

	// A hit records only retrieval.injected -- no second miss row appears.
	rec = callJSON(t, ctx, cli, "recall", map[string]any{"query": "chroma readiness boot race"})
	require.NotEmpty(t, rec["hits"])
	require.Len(t, missEvents(t, db), 1)
}
