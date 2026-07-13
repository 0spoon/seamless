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
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/store"
)

func writePlanNote(t *testing.T, mgr *files.Manager, slug, planSlug, title, status string, updated time.Time) core.Note {
	t.Helper()
	n, err := mgr.WriteNote(context.Background(), core.Note{
		ID: mustID(t), Slug: slug, Title: title, Project: "demo",
		Description: "captured plan", Body: "# " + title + "\n\nbody",
		Tags:    []string{"plan:" + planSlug, plans.TagPlan, "plan-status:" + status, "created-by:agent"},
		Created: updated, Updated: updated,
	})
	require.NoError(t, err)
	return n
}

func TestStalePlanProposeAndApply(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mgr, err := files.NewManager(dir, db, slog.Default())
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()
	rec := events.NewRecorder(db)

	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	staleAt := now.AddDate(0, 0, -20)
	freshAt := now.AddDate(0, 0, -2)

	writePlanNote(t, mgr, "cc-plan-old", "old-plan", "Old Plan", plans.StatusPresented, staleAt)
	writePlanNote(t, mgr, "cc-plan-new", "new-plan", "New Plan", plans.StatusDraft, freshAt)
	writePlanNote(t, mgr, "cc-plan-done", "done-plan", "Done Plan", plans.StatusApproved, staleAt)

	g := New(db, mgr, nil, nil, rec, Config{StalePlanDays: 14}, slog.Default())
	g.now = func() time.Time { return now }

	seen, err := store.AllProposalKeys(ctx, db)
	require.NoError(t, err)
	n, err := g.proposeStalePlans(ctx, seen)
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the stale unapproved plan is proposed")

	props, err := store.PendingProposals(ctx, db, store.ProposalAbandonPlan)
	require.NoError(t, err)
	require.Len(t, props, 1)
	p := props[0]
	require.Equal(t, "old-plan", p.Payload["slug"])
	require.Equal(t, "cc-plan-old", p.Payload["note_slug"])
	require.Equal(t, "presented", p.Payload["plan_status"])

	// Apply retags the note abandoned.
	result, err := g.Apply(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, "cc-plan-old", result["abandoned"])
	idx, ok, err := store.NoteBySlug(ctx, db, "demo", "cc-plan-old")
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, idx.Tags, "plan-status:abandoned")
	require.Contains(t, idx.Description, "abandoned")

	// A second pass proposes nothing new: the abandoned plan is settled and the
	// applied key stays known.
	seen, err = store.AllProposalKeys(ctx, db)
	require.NoError(t, err)
	n, err = g.proposeStalePlans(ctx, seen)
	require.NoError(t, err)
	require.Zero(t, n)

	// StalePlanDays 0 disables the pass.
	g0 := New(db, mgr, nil, nil, rec, Config{}, slog.Default())
	g0.now = func() time.Time { return now }
	n, err = g0.proposeStalePlans(ctx, map[string]struct{}{})
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestAbandonPlanApplyGuardsApprovedPlans(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mgr, err := files.NewManager(dir, db, slog.Default())
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	note := writePlanNote(t, mgr, "cc-plan-raced", "raced-plan", "Raced Plan", plans.StatusDraft, now.AddDate(0, 0, -20))

	g := New(db, mgr, nil, nil, nil, Config{StalePlanDays: 14}, slog.Default())
	g.now = func() time.Time { return now }
	seen := map[string]struct{}{}
	n, err := g.proposeStalePlans(ctx, seen)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	props, err := store.PendingProposals(ctx, db, store.ProposalAbandonPlan)
	require.NoError(t, err)
	require.Len(t, props, 1)

	// The plan gets approved after the proposal was raised.
	note.Tags = plans.SetStatusTag(note.Tags, plans.StatusApproved)
	_, err = mgr.WriteNote(ctx, note)
	require.NoError(t, err)

	// Apply refuses and leaves the proposal pending.
	_, err = g.Apply(ctx, props[0].ID)
	require.ErrorContains(t, err, "approved since")
	p, ok, err := store.ProposalByID(ctx, db, props[0].ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, store.ProposalPending, p.Status)
}
