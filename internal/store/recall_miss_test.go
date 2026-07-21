package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// insertMissEvent writes a raw event row with a project slug and payload,
// reusing insertProjectEvent (utility_activation_test.go) with no item id.
func insertMissEvent(t *testing.T, db *sql.DB, kind core.EventKind, session, project, payload string, ts time.Time) {
	t.Helper()
	insertProjectEvent(t, db, kind, session, project, "", payload, ts)
}

func TestRecallMissesSince(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	insertMissEvent(t, db, core.EventRecallMiss, "S1", "proj", `{"query":"older miss"}`, now.Add(-2*time.Hour))
	insertMissEvent(t, db, core.EventRecallMiss, "S2", "proj", `{"query":"newer miss"}`, now.Add(-1*time.Hour))
	insertMissEvent(t, db, core.EventRecallMiss, "S3", "proj", `{}`, now.Add(-30*time.Minute))                           // no query -> skipped
	insertMissEvent(t, db, core.EventRecallMiss, "S4", "proj", `{"query":"ancient miss"}`, now.Add(-72*time.Hour))       // outside window
	insertMissEvent(t, db, core.EventInjected, "S5", "proj", `{"query":"a hit","source":"recall"}`, now.Add(-time.Hour)) // wrong kind

	got, err := RecallMissesSince(ctx, db, now.Add(-24*time.Hour))
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "older miss", got[0].Query, "oldest first")
	require.Equal(t, "S1", got[0].SessionID)
	require.Equal(t, "proj", got[0].Project)
	require.Equal(t, "newer miss", got[1].Query)
	require.WithinDuration(t, now.Add(-2*time.Hour), got[0].TS, time.Second)
}

func TestRecallHitQueriesSince(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	insertMissEvent(t, db, core.EventInjected, "S1", "proj",
		`{"query":"found it","item_ids":["A"],"source":"recall"}`, now.Add(-time.Hour))
	insertMissEvent(t, db, core.EventInjected, "S2", "proj",
		`{"item_ids":["B"],"hook":"session-start"}`, now.Add(-time.Hour)) // briefing, no query -> skipped
	insertMissEvent(t, db, core.EventInjected, "S3", "proj",
		`{"query":"old hit","item_ids":["C"],"source":"recall"}`, now.Add(-72*time.Hour)) // outside window

	got, err := RecallHitQueriesSince(ctx, db, now.Add(-24*time.Hour))
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, RecallHitQuery{Project: "proj", Query: "found it"}, got[0])
}

func TestToolErrorsSince(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	insertMissEvent(t, db, core.EventToolCall, "S1", "proj",
		`{"tool":"tasks_add","is_error":true,"error":"tasks_add: title is required","args":{"titel":"x"}}`, now.Add(-2*time.Hour))
	insertMissEvent(t, db, core.EventToolCall, "S2", "proj",
		`{"tool":"recall","is_error":true,"error":"recall: invalid scope \"memoires\""}`, now.Add(-time.Hour))
	insertMissEvent(t, db, core.EventToolCall, "S3", "proj",
		`{"tool":"recall","result":"ok"}`, now.Add(-time.Hour)) // success -> skipped
	insertMissEvent(t, db, core.EventToolCall, "S4", "proj",
		`{"tool":"tasks_add","is_error":true,"error":"stale"}`, now.Add(-72*time.Hour)) // outside window
	insertMissEvent(t, db, core.EventHookError, "S5", "proj",
		`{"stage":"ambient-create","error":"boom"}`, now.Add(-time.Hour)) // wrong kind

	got, err := ToolErrorsSince(ctx, db, now.Add(-24*time.Hour))
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "tool", got[0].Surface)
	require.Equal(t, "tasks_add", got[0].Key, "oldest first")
	require.Equal(t, "tasks_add: title is required", got[0].Error)
	require.Equal(t, "S1", got[0].SessionID)
	require.Equal(t, "proj", got[0].Project)
	require.Equal(t, map[string]any{"titel": "x"}, got[0].Args)
	require.Equal(t, "recall", got[1].Key)
	require.Nil(t, got[1].Args)
}

func TestHookErrorsSince(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	insertMissEvent(t, db, core.EventHookError, "", "proj",
		`{"stage":"ambient-create","error":"store: session name already exists","client":"claude-code"}`, now.Add(-2*time.Hour))
	insertMissEvent(t, db, core.EventHookError, "S1", "proj",
		`{"stage":"prompt-recall","error":"context deadline exceeded"}`, now.Add(-time.Hour))
	insertMissEvent(t, db, core.EventHookError, "S2", "proj", `{"stage":"no-error"}`, now.Add(-time.Hour))           // no error -> skipped
	insertMissEvent(t, db, core.EventHookError, "S3", "proj", `{"stage":"old","error":"x"}`, now.Add(-72*time.Hour)) // outside window
	insertMissEvent(t, db, core.EventToolCall, "S4", "proj",
		`{"tool":"recall","is_error":true,"error":"nope"}`, now.Add(-time.Hour)) // wrong kind

	got, err := HookErrorsSince(ctx, db, now.Add(-24*time.Hour))
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "hook", got[0].Surface)
	require.Equal(t, "ambient-create", got[0].Key, "oldest first")
	require.Equal(t, "store: session name already exists", got[0].Error)
	require.Empty(t, got[0].SessionID, "hook errors are often unattributed")
	require.Equal(t, "prompt-recall", got[1].Key)
}

func TestBuildRetrievalReport_ToolMisses(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	insertMissEvent(t, db, core.EventRecallMiss, "S1", "proj", `{"query":"gap one"}`, now.Add(-time.Hour))
	insertMissEvent(t, db, core.EventRecallMiss, "S2", "proj", `{"query":"gap two"}`, now.Add(-2*time.Hour))
	insertMissEvent(t, db, core.EventRecallMiss, "S3", "proj", `{"query":"long ago"}`, now.Add(-40*24*time.Hour))

	rep, err := BuildRetrievalReport(ctx, db, ResolveRetrievalWindow("7d", now), 12)
	require.NoError(t, err)
	require.Equal(t, 2, rep.ToolMisses)
	require.Zero(t, rep.MissRate, "tool misses stay out of the prompt-recall miss rate")
}

func TestCreateProposal_AcceptsEveryCanonicalKind(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, kind := range ProposalKinds {
		_, err := CreateProposal(ctx, db, kind, map[string]any{"key": "k:" + kind})
		require.NoError(t, err, "kind %q must pass the gardener_proposals CHECK constraint", kind)
	}
}
