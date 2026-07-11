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
