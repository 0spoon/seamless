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

// Mishap texts are scanned once, at ingestion, for exact slug mentions of the
// project's active memories (own + global); matches persist in the event
// payload as item_ids. Another project's memory never matches, and a report
// naming nothing carries no item_ids key at all.
func TestSessionEnd_LinksMishapsToNamedMemories(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	sessStart := callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})
	require.Equal(t, "demo", sessStart["project"])

	write := func(name, project string) string {
		args := map[string]any{
			"name": name, "kind": "gotcha",
			"description": "mishap linkage fixture", "body": "body\n",
		}
		if project != "" {
			args["project"] = project
		}
		w := callJSON(t, ctx, cli, "memory_write", args)
		return w["id"].(string)
	}
	pkillID := write("pkill-target-check", "")                  // bound project (demo)
	configID := write("installed-config-is-a-snapshot", "")     // bound project (demo)
	globalID := write("global-push-requires-consent", "global") // global: visible to demo
	_ = write("other-repo-flag-order", "elsewhere")             // another project: must never match

	end := callJSON(t, ctx, cli, "session_end", map[string]any{
		"findings": "done",
		"mishaps": []string{
			"violated pkill-target-check: pkill -f matched the live daemon",
			"no stored memory names this one",
			"ignored pkill-target-check and installed-config-is-a-snapshot in one go",
			"mentioned other-repo-flag-order, which lives in another project",
			"pushed without asking despite global-push-requires-consent",
		},
	})
	require.Equal(t, float64(5), end["mishaps_recorded"])

	got := mishapEvents(t, db)
	require.Len(t, got, 5)
	// Same-ms ULIDs do not guarantee insertion order, so index by description.
	byDesc := map[string]map[string]any{}
	for _, m := range got {
		byDesc[m["description"].(string)] = m
	}
	itemIDs := func(desc string) []string {
		m, ok := byDesc[desc]
		require.True(t, ok, "no mishap event with description %q", desc)
		raw, present := m["item_ids"]
		if !present {
			return nil
		}
		arr, ok := raw.([]any)
		require.True(t, ok, "item_ids must be a JSON array, got %T", raw)
		out := make([]string, len(arr))
		for i, v := range arr {
			s, ok := v.(string)
			require.True(t, ok, "item_ids[%d] must be a string, got %T", i, v)
			out[i] = s
		}
		return out
	}

	require.Equal(t, []string{pkillID},
		itemIDs("violated pkill-target-check: pkill -f matched the live daemon"))
	require.ElementsMatch(t, []string{pkillID, configID},
		itemIDs("ignored pkill-target-check and installed-config-is-a-snapshot in one go"))
	require.Equal(t, []string{globalID},
		itemIDs("pushed without asking despite global-push-requires-consent"))

	// A report naming nothing carries no item_ids key at all -- not an empty array.
	noMatch, ok := byDesc["no stored memory names this one"]
	require.True(t, ok)
	require.NotContains(t, noMatch, "item_ids")
	// Another project's memory name is not part of demo's corpus.
	crossProject, ok := byDesc["mentioned other-repo-flag-order, which lives in another project"]
	require.True(t, ok)
	require.NotContains(t, crossProject, "item_ids")
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
