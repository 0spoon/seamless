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

	// Keys span every status (used by the gardener to avoid re-proposing). A
	// pending proposal is a hard block: nothing re-raises what is already asked.
	keys, err := ProposalKeyBlocks(ctx, db)
	require.NoError(t, err)
	require.True(t, keys["archive:m1"].Hard)

	// Resolve moves it out of pending and stamps resolved_at.
	require.NoError(t, ResolveProposal(ctx, db, p.ID, ProposalApplied, time.Now().UTC()))
	require.Empty(t, mustPending(t, db, ""))

	got, ok, err := ProposalByID(ctx, db, p.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ProposalApplied, got.Status)
	require.NotNil(t, got.ResolvedAt)

	// The key is still known after resolution (never re-proposed).
	keys, err = ProposalKeyBlocks(ctx, db)
	require.NoError(t, err)
	require.True(t, keys["archive:m1"].Hard)

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
	keys, err := ProposalKeyBlocks(ctx, db)
	require.NoError(t, err)
	require.True(t, keys["digest:seamless:2026-07"].Hard)

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

// A rejection has two strengths, and the store is where they differ: a
// dismissal records WHEN the owner decided, so a pass can ask whether its
// evidence is newer, while a hide records that nothing newer will matter.
func TestProposalKeyBlocks_RejectionTiers(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	dismissed, err := CreateProposal(ctx, db, ProposalToolError, map[string]any{"key": "err:a"})
	require.NoError(t, err)
	hidden, err := CreateProposal(ctx, db, ProposalToolError, map[string]any{"key": "err:b"})
	require.NoError(t, err)
	require.NoError(t, ResolveProposal(ctx, db, dismissed.ID, ProposalDismissed, at))
	require.NoError(t, ResolveProposal(ctx, db, hidden.ID, ProposalHidden, at))

	blocks, err := ProposalKeyBlocks(ctx, db)
	require.NoError(t, err)
	require.False(t, blocks["err:a"].Hard, "a dismissal is a soft block")
	require.True(t, at.Equal(blocks["err:a"].DismissedAt), "and carries the moment to measure recurrence from")
	require.True(t, blocks["err:b"].Hard, "a hide is not")

	// A key re-raised after its dismissal has two rows. The pending one wins:
	// nothing re-proposes what is already in the queue.
	reraised, err := CreateProposal(ctx, db, ProposalToolError, map[string]any{"key": "err:a"})
	require.NoError(t, err)
	blocks, err = ProposalKeyBlocks(ctx, db)
	require.NoError(t, err)
	require.True(t, blocks["err:a"].Hard, "the strongest rule across a key's rows wins")

	// Dismissing that one too leaves the key soft again, at the LATER stamp.
	later := at.Add(48 * time.Hour)
	require.NoError(t, ResolveProposal(ctx, db, reraised.ID, ProposalDismissed, later))
	blocks, err = ProposalKeyBlocks(ctx, db)
	require.NoError(t, err)
	require.False(t, blocks["err:a"].Hard)
	require.True(t, later.Equal(blocks["err:a"].DismissedAt), "the newest dismissal is the one to beat")
}

// Hiding is reversible two ways, and they are not the same operation: unhide
// lifts the block while leaving the proposal resolved, undo puts the proposal
// itself back in the queue.
func TestHiddenProposals_UnhideAndReopen(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	p, err := CreateProposal(ctx, db, ProposalToolError, map[string]any{"key": "err:a"})
	require.NoError(t, err)
	require.NoError(t, ResolveProposal(ctx, db, p.ID, ProposalHidden, at))

	hidden, err := HiddenProposals(ctx, db)
	require.NoError(t, err)
	require.Len(t, hidden, 1)
	require.Equal(t, p.ID, hidden[0].ID)
	require.NotNil(t, hidden[0].ResolvedAt)

	// Unhide demotes to a regular dismissal, keeping the moment of the decision
	// -- that stamp is what a recurrence is measured against.
	require.NoError(t, UnhideProposal(ctx, db, p.ID))
	got, ok, err := ProposalByID(ctx, db, p.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ProposalDismissed, got.Status)
	require.True(t, at.Equal(*got.ResolvedAt), "unhide does not restamp the decision")
	require.Empty(t, mustPending(t, db, ""), "and does not return it to the queue")

	empty, err := HiddenProposals(ctx, db)
	require.NoError(t, err)
	require.Empty(t, empty)

	// Unhiding something that is not hidden is an error, not a silent no-op.
	require.Error(t, UnhideProposal(ctx, db, p.ID))

	// A hide is undoable like any other rejection: reopening returns it. (The
	// proposal is dismissed by now, so it goes back through the queue first --
	// only a pending proposal can be resolved into a tier.)
	require.NoError(t, ReopenProposal(ctx, db, p.ID))
	require.NoError(t, ResolveProposal(ctx, db, p.ID, ProposalHidden, at))
	require.Error(t, ResolveProposal(ctx, db, p.ID, ProposalHidden, at), "already resolved")
	require.NoError(t, ReopenProposal(ctx, db, p.ID))
	require.Len(t, mustPending(t, db, ""), 1)
}

func mustPending(t *testing.T, db *sql.DB, kind string) []Proposal {
	t.Helper()
	ps, err := PendingProposals(context.Background(), db, kind)
	require.NoError(t, err)
	return ps
}
