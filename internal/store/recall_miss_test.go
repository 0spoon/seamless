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
