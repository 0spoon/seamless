package mcp_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// mishapEvents reads the agent.mishap rows straight off the event log.
func mishapEvents(t *testing.T, db *sql.DB) []map[string]any {
	t.Helper()
	rows, err := db.Query(`SELECT project_slug, session_id, payload FROM events WHERE kind = ? ORDER BY id`,
		string(core.EventAgentMishap))
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

func TestSessionEnd_RecordsSelfReportedMishaps(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	start := callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})
	sessID := start["session_id"].(string)

	end := callJSON(t, ctx, cli, "session_end", map[string]any{
		"findings": "done",
		"mishaps": []string{
			"pkill -f matched the live daemon as well as the scratch one",
			"  edited the installed config instead of the repo copy  ",
		},
	})
	require.Equal(t, "completed", end["status"])
	require.Equal(t, float64(2), end["mishaps_recorded"])

	got := mishapEvents(t, db)
	require.Len(t, got, 2)
	descriptions := []string{got[0]["description"].(string), got[1]["description"].(string)}
	// Array items are trimmed by argument coercion before recording. Same-ms
	// ULIDs do not guarantee insertion order, so assert membership, not order.
	require.ElementsMatch(t, []string{
		"pkill -f matched the live daemon as well as the scratch one",
		"edited the installed config instead of the repo copy",
	}, descriptions)
	for _, m := range got {
		require.Equal(t, "demo", m["_project"])
		require.Equal(t, sessID, m["_session"])
	}
}

func TestSessionEnd_NoMishapsRecordsNothing(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})
	end := callJSON(t, ctx, cli, "session_end", map[string]any{"findings": "done"})
	require.Equal(t, "completed", end["status"])
	require.Equal(t, float64(0), end["mishaps_recorded"])
	require.Empty(t, mishapEvents(t, db))
}
