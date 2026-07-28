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

// The apply result round-trips through the result column, and ReopenProposal
// returns a resolved proposal to the queue -- clearing both the resolution
// stamp and the now-meaningless result, while leaving the dedup key intact.
func TestProposalResultAndReopen(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	p, err := CreateProposal(ctx, db, ProposalDigest, map[string]any{"key": "digest:seamless:2026-07"})
	require.NoError(t, err)
	require.Nil(t, p.Result, "a fresh proposal has no result")

	// A pending proposal cannot be reopened -- only a resolved one.
	require.Error(t, ReopenProposal(ctx, db, p.ID))

	require.NoError(t, ResolveProposal(ctx, db, p.ID, ProposalApplied, time.Now().UTC()))
	require.NoError(t, RecordProposalResult(ctx, db, p.ID, map[string]any{
		"note_id": "01NOTE", "title": "seamless digest 2026-07",
	}))

	got, ok, err := ProposalByID(ctx, db, p.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "01NOTE", got.Result["note_id"])

	require.NoError(t, ReopenProposal(ctx, db, p.ID))
	got, _, err = ProposalByID(ctx, db, p.ID)
	require.NoError(t, err)
	require.Equal(t, ProposalPending, got.Status)
	require.Nil(t, got.ResolvedAt, "reopening clears the resolution stamp")
	require.Nil(t, got.Result, "reopening clears the stale apply result")
	require.Len(t, mustPending(t, db, ""), 1, "the proposal is back in the queue")

	// Dedup is unaffected: the key survives the whole round trip.
	keys, err := AllProposalKeys(ctx, db)
	require.NoError(t, err)
	_, ok = keys["digest:seamless:2026-07"]
	require.True(t, ok)

	// A second undo of the same proposal is refused (it is pending again).
	require.Error(t, ReopenProposal(ctx, db, p.ID))
	// So is undoing something that never existed.
	require.Error(t, ReopenProposal(ctx, db, "01NOSUCHPROPOSAL"))
}

// RecentResolvedProposals lists only resolved proposals, newest resolution
// first, capped at the limit -- the ordering the console's "Recently decided"
// section relies on.
func TestRecentResolvedProposals_OrdersByResolution(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	// Create oldest-first, resolve in the same order, so "newest resolved first"
	// is genuinely the reverse of creation rather than an accident of id order.
	var ids []string
	for i := range 4 {
		p, err := CreateProposal(ctx, db, ProposalArchive, map[string]any{"key": "archive:m" + string(rune('a'+i))})
		require.NoError(t, err)
		ids = append(ids, p.ID)
	}
	require.NoError(t, ResolveProposal(ctx, db, ids[0], ProposalApplied, base))
	require.NoError(t, ResolveProposal(ctx, db, ids[1], ProposalDismissed, base.Add(time.Minute)))
	require.NoError(t, ResolveProposal(ctx, db, ids[2], ProposalApplied, base.Add(2*time.Minute)))
	// ids[3] stays pending.

	recent, err := RecentResolvedProposals(ctx, db, 10)
	require.NoError(t, err)
	require.Len(t, recent, 3, "the pending proposal is not 'recently decided'")
	require.Equal(t, []string{ids[2], ids[1], ids[0]}, []string{recent[0].ID, recent[1].ID, recent[2].ID})
	require.Equal(t, ProposalDismissed, recent[1].Status)

	capped, err := RecentResolvedProposals(ctx, db, 2)
	require.NoError(t, err)
	require.Len(t, capped, 2)
	require.Equal(t, ids[2], capped[0].ID)

	none, err := RecentResolvedProposals(ctx, db, 0)
	require.NoError(t, err)
	require.Empty(t, none)
}

func mustPending(t *testing.T, db *sql.DB, kind string) []Proposal {
	t.Helper()
	ps, err := PendingProposals(context.Background(), db, kind)
	require.NoError(t, err)
	return ps
}
