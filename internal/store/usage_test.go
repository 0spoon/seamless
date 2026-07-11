package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

func TestGetUsageSummary(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedMemoryRow(t, db, "m1", "alpha", now)
	seedMemoryRow(t, db, "m2", "beta", now)
	require.NoError(t, CreateSession(ctx, db, core.Session{
		ID: "s1", Name: "cc/a", Status: core.SessionCompleted, Findings: "x", CreatedAt: now, UpdatedAt: now,
	}))
	_, err := CreateProposal(ctx, db, ProposalArchive, map[string]any{"key": "archive:m1"})
	require.NoError(t, err)

	// Injection + read events -> retrieval stats.
	insertEvent(t, db, core.EventInjected, "", `{"item_ids":["m1","m2"]}`, now)
	insertEvent(t, db, core.EventInjected, "", `{"item_ids":["m1"]}`, now)
	insertEvent(t, db, core.EventMemoryRead, "m1", "{}", now)
	require.NoError(t, RebuildRetrievalStats(ctx, db))

	u, err := GetUsageSummary(ctx, db)
	require.NoError(t, err)
	require.Equal(t, 2, u.Memories.Active)
	require.Equal(t, 2, u.Memories.ByKind["gotcha"])
	require.Equal(t, 1, u.Sessions["completed"])
	require.Equal(t, 1, u.GardenerPending["archive"])
	require.Equal(t, 3, u.Retrieval.Injections) // m1 x2 + m2 x1
	require.Equal(t, 1, u.Retrieval.Reads)
	require.NotEmpty(t, u.Retrieval.TopInjected)
	require.Equal(t, "m1", u.Retrieval.TopInjected[0].ID) // most-injected first
	require.Equal(t, 2, u.Retrieval.TopInjected[0].Count)
	require.Equal(t, 2, u.EventsByKind["retrieval.injected"])
}
