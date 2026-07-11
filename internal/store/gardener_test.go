package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProposalLifecycle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	p, err := CreateProposal(ctx, db, ProposalArchive, map[string]any{"key": "archive:m1", "name": "m1"})
	require.NoError(t, err)
	require.Equal(t, ProposalPending, p.Status)
	require.NotEmpty(t, p.ID)

	pending, err := PendingProposals(ctx, db, "")
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "m1", pending[0].Payload["name"])

	// Kind filter.
	require.Empty(t, mustPending(t, db, ProposalMerge))
	require.Len(t, mustPending(t, db, ProposalArchive), 1)

	// Keys span every status (used by the gardener to avoid re-proposing).
	keys, err := AllProposalKeys(ctx, db)
	require.NoError(t, err)
	_, ok := keys["archive:m1"]
	require.True(t, ok)

	// Resolve moves it out of pending and stamps resolved_at.
	require.NoError(t, ResolveProposal(ctx, db, p.ID, ProposalApplied, time.Now().UTC()))
	require.Empty(t, mustPending(t, db, ""))

	got, ok, err := ProposalByID(ctx, db, p.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ProposalApplied, got.Status)
	require.NotNil(t, got.ResolvedAt)

	// The key is still known after resolution (never re-proposed).
	keys, err = AllProposalKeys(ctx, db)
	require.NoError(t, err)
	_, ok = keys["archive:m1"]
	require.True(t, ok)

	// Resolving an already-resolved proposal is an error.
	require.Error(t, ResolveProposal(ctx, db, p.ID, ProposalDismissed, time.Now().UTC()))
	// Invalid status is rejected.
	require.Error(t, ResolveProposal(ctx, db, p.ID, "bogus", time.Now().UTC()))
}

func mustPending(t *testing.T, db *sql.DB, kind string) []Proposal {
	t.Helper()
	ps, err := PendingProposals(context.Background(), db, kind)
	require.NoError(t, err)
	return ps
}
