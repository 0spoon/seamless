package gardener

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/store"
)

func newApplyFixture(t *testing.T) (*Service, *files.Manager, func() context.Context) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mgr, err := files.NewManager(dir, db, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	g := New(db, mgr, nil, nil, events.NewRecorder(db), Config{}, slog.Default())
	return g, mgr, func() context.Context { return context.Background() }
}

func TestApplyMerge_SupersedesDropByKeep(t *testing.T) {
	g, mgr, cx := newApplyFixture(t)
	ctx := cx()
	now := time.Now().UTC()

	writeMem(t, mgr, "keep-me", "", "1", core.KindGotcha, now, "newer copy")
	writeMem(t, mgr, "drop-me", "", "1", core.KindGotcha, now.Add(-time.Hour), "older copy")
	keep, _, err := store.MemoryByName(ctx, g.db, "", "keep-me")
	require.NoError(t, err)
	drop, _, err := store.MemoryByName(ctx, g.db, "", "drop-me")
	require.NoError(t, err)

	p, err := store.CreateProposal(ctx, g.db, store.ProposalMerge, map[string]any{
		"key":  mergeKey(keep.ID, drop.ID),
		"keep": map[string]any{"id": keep.ID, "name": keep.Name},
		"drop": map[string]any{"id": drop.ID, "name": drop.Name},
	})
	require.NoError(t, err)

	res, err := g.Apply(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, "applied", res["status"])

	// drop is superseded (leaves the active index); keep remains active.
	_, found, err := store.MemoryByName(ctx, g.db, "", "drop-me")
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = store.MemoryByName(ctx, g.db, "", "keep-me")
	require.NoError(t, err)
	require.True(t, found)

	// The dropped memory records its successor.
	dropIdx, _, err := store.MemoryByNameIncludingInvalid(ctx, g.db, "", "drop-me")
	require.NoError(t, err)
	require.Equal(t, keep.ID, dropIdx.SupersededBy)
}

func TestApplyConsolidate_WritesUnifiedAndSupersedesSources(t *testing.T) {
	g, mgr, cx := newApplyFixture(t)
	ctx := cx()
	now := time.Now().UTC()

	writeMem(t, mgr, "dfu-a", "seamless", "1", core.KindGotcha, now, "part a")
	writeMem(t, mgr, "dfu-b", "seamless", "2", core.KindGotcha, now.Add(-time.Hour), "part b")
	a, _, err := store.MemoryByName(ctx, g.db, "seamless", "dfu-a")
	require.NoError(t, err)
	b, _, err := store.MemoryByName(ctx, g.db, "seamless", "dfu-b")
	require.NoError(t, err)

	p, err := store.CreateProposal(ctx, g.db, store.ProposalConsolidate, map[string]any{
		"key": "consolidate:" + a.ID + "|" + b.ID, "name": "dfu-unified", "kind": "runbook",
		"project": "seamless", "description": "the unified DFU flow", "body": "# DFU\ncombined steps",
		"sources": []any{
			map[string]any{"id": a.ID, "name": a.Name},
			map[string]any{"id": b.ID, "name": b.Name},
		},
	})
	require.NoError(t, err)

	res, err := g.Apply(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, "applied", res["status"])

	// The new unified memory is active with the requested kind.
	unified, found, err := store.MemoryByName(ctx, g.db, "seamless", "dfu-unified")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, core.MemoryKind("runbook"), unified.Kind)

	// Both sources are superseded by it (leave the active index, point at unified).
	for _, name := range []string{"dfu-a", "dfu-b"} {
		_, active, err := store.MemoryByName(ctx, g.db, "seamless", name)
		require.NoError(t, err)
		require.False(t, active, name+" should be superseded")
		idx, _, err := store.MemoryByNameIncludingInvalid(ctx, g.db, "seamless", name)
		require.NoError(t, err)
		require.Equal(t, unified.ID, idx.SupersededBy)
	}
}

// TestApplyConsolidate_RetryIsIdempotent drives applyConsolidate twice (as a
// retry after Apply left the proposal pending on a partial failure would): it
// must converge -- reuse the already-written unified memory and skip
// already-superseded sources -- not write a second copy.
func TestApplyConsolidate_RetryIsIdempotent(t *testing.T) {
	g, mgr, cx := newApplyFixture(t)
	ctx := cx()
	now := time.Now().UTC()

	writeMem(t, mgr, "src-a", "", "1", core.KindGotcha, now, "a")
	writeMem(t, mgr, "src-b", "", "2", core.KindGotcha, now, "b")
	a, _, err := store.MemoryByName(ctx, g.db, "", "src-a")
	require.NoError(t, err)
	b, _, err := store.MemoryByName(ctx, g.db, "", "src-b")
	require.NoError(t, err)

	p := store.Proposal{Payload: map[string]any{
		"name": "unified", "kind": "reference", "project": "", "description": "u", "body": "combined",
		"sources": []any{
			map[string]any{"id": a.ID, "name": "src-a"},
			map[string]any{"id": b.ID, "name": "src-b"},
		},
	}}

	_, err = g.applyConsolidate(ctx, p, now)
	require.NoError(t, err)
	_, err = g.applyConsolidate(ctx, p, now.Add(time.Minute))
	require.NoError(t, err, "a second apply converges rather than erroring")

	// Exactly one active "unified" memory exists -- no duplicate from the retry.
	all, err := store.AllActiveMemories(ctx, g.db)
	require.NoError(t, err)
	n := 0
	for _, m := range all {
		if m.Name == "unified" {
			n++
		}
	}
	require.Equal(t, 1, n, "retry must not create a duplicate unified memory")
}

func TestApplyDigest_WritesNote(t *testing.T) {
	g, _, cx := newApplyFixture(t)
	ctx := cx()

	p, err := store.CreateProposal(ctx, g.db, store.ProposalDigest, map[string]any{
		"key": "digest::2026-07", "project": "", "title": "Session digest -- 2026-07",
		"body": "## July\n- shipped the gardener",
	})
	require.NoError(t, err)

	res, err := g.Apply(ctx, p.ID)
	require.NoError(t, err)
	noteID, _ := res["note_id"].(string)
	require.NotEmpty(t, noteID)

	note, ok, err := store.NoteByID(ctx, g.db, noteID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "Session digest -- 2026-07", note.Title)
}

func TestApplyDismiss(t *testing.T) {
	g, _, cx := newApplyFixture(t)
	ctx := cx()

	p, err := store.CreateProposal(ctx, g.db, store.ProposalArchive, map[string]any{
		"key": "archive:gone", "id": "gone", "name": "gone",
	})
	require.NoError(t, err)

	require.NoError(t, g.Dismiss(ctx, p.ID))
	got, ok, err := store.ProposalByID(ctx, g.db, p.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, store.ProposalDismissed, got.Status)

	// Applying a proposal whose target memory does not exist errors and leaves it
	// pending (here the proposal is already dismissed, so this also covers the
	// "not pending" guard).
	_, err = g.Apply(ctx, p.ID)
	require.Error(t, err)
}

func TestApplyArchive_MissingMemoryErrors(t *testing.T) {
	g, _, cx := newApplyFixture(t)
	ctx := cx()
	p, err := store.CreateProposal(ctx, g.db, store.ProposalArchive, map[string]any{
		"key": "archive:ghost", "id": "ghost", "name": "ghost",
	})
	require.NoError(t, err)

	_, err = g.Apply(ctx, p.ID)
	require.Error(t, err)
	// The proposal stays pending so the owner can dismiss it.
	got, _, err := store.ProposalByID(ctx, g.db, p.ID)
	require.NoError(t, err)
	require.Equal(t, store.ProposalPending, got.Status)
}
