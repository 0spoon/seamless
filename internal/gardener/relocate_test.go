package gardener

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/store"
)

// writeStampedMem writes a memory carrying a source_session provenance stamp,
// which is what the isolation audit traces back to a project.
func writeStampedMem(t *testing.T, mgr *files.Manager, name, project, stamp string, at time.Time) core.Memory {
	t.Helper()
	m, err := mgr.WriteMemory(context.Background(), core.Memory{
		ID: mustID(t), Kind: core.KindGotcha, Name: name, Description: name + " description",
		Project: project, Body: "body of " + name, SourceSession: stamp,
		Created: at, Updated: at, ValidFrom: at,
	})
	require.NoError(t, err)
	return m
}

// seedLeakFixture builds the world a tighten walks into: a confidential project
// whose sessions wrote two memories into the global scope, plus the memories
// that must NOT be swept up with them.
func seedLeakFixture(t *testing.T, g *Service, mgr *files.Manager) (ctx context.Context, leaked []core.Memory) {
	t.Helper()
	ctx = context.Background()
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	for _, slug := range []string{"acme", "other"} {
		_, err := store.EnsureProject(ctx, g.db, slug, slug)
		require.NoError(t, err)
	}
	require.NoError(t, store.CreateSession(ctx, g.db, core.Session{
		ID: "01JACMESESSION0000000000", Name: "cc/acme1", ProjectSlug: "acme",
		Status: core.SessionActive, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, store.CreateSession(ctx, g.db, core.Session{
		ID: "01JOTHERSESSION000000000", Name: "cc/other1", ProjectSlug: "other",
		Status: core.SessionActive, CreatedAt: now, UpdatedAt: now,
	}))

	bound := writeStampedMem(t, mgr, "bound-leak", "", "01JACMESESSION0000000000", now)
	ambient := writeStampedMem(t, mgr, "ambient-leak", "", "cc/acme1", now.Add(-time.Minute))
	writeStampedMem(t, mgr, "other-leak", "", "cc/other1", now)
	writeStampedMem(t, mgr, "already-inside", "acme", "cc/acme1", now)

	require.NoError(t, store.SetProjectIsolation(ctx, g.db, "acme", core.IsolationConfidential))
	return ctx, []core.Memory{bound, ambient}
}

func TestProposeIsolationRelocations_FilesOnePerLeakAndDedups(t *testing.T) {
	g, mgr, _ := newApplyFixture(t)
	ctx, leaked := seedLeakFixture(t, g, mgr)

	n, err := g.ProposeIsolationRelocations(ctx, "acme")
	require.NoError(t, err)
	require.Equal(t, 2, n, "one proposal per global memory this project's sessions wrote")

	pending, err := store.PendingProposals(ctx, g.db, store.ProposalRelocate)
	require.NoError(t, err)
	require.Len(t, pending, 2)

	byID := map[string]store.Proposal{}
	for _, p := range pending {
		byID[payloadString(p.Payload, "id")] = p
	}
	for _, mem := range leaked {
		p, ok := byID[mem.ID]
		require.True(t, ok, "memory %q must have a relocation proposal", mem.Name)
		require.Equal(t, mem.Name, payloadString(p.Payload, "name"))
		require.Equal(t, "", payloadString(p.Payload, "from"), "a leak always starts global")
		require.Equal(t, "acme", payloadString(p.Payload, "to"))
		require.Equal(t, mem.SourceSession, payloadString(p.Payload, "session"),
			"the stamp that traced it is the evidence")
		require.Equal(t, string(core.IsolationConfidential), payloadString(p.Payload, "isolation"))
		require.Contains(t, payloadString(p.Payload, "reason"), "acme")
		require.Equal(t, relocateKey("acme", mem.ID), payloadString(p.Payload, "key"))
	}

	// Nothing was moved: the audit proposes, the owner applies.
	for _, mem := range leaked {
		idx, found, err := store.MemoryByID(ctx, g.db, mem.ID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "", idx.Project, "a propose-only pass never moves a memory")
	}

	// A second tighten (confidential -> sealed) asks nothing twice.
	require.NoError(t, store.SetProjectIsolation(ctx, g.db, "acme", core.IsolationSealed))
	n, err = g.ProposeIsolationRelocations(ctx, "acme")
	require.NoError(t, err)
	require.Zero(t, n, "the dedup key is per (project, memory), not per fence state")

	// A dismissed question stays answered.
	require.NoError(t, g.Dismiss(ctx, pending[0].ID))
	n, err = g.ProposeIsolationRelocations(ctx, "acme")
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestProposeIsolationRelocations_RefusesAnOpenProject(t *testing.T) {
	g, mgr, _ := newApplyFixture(t)
	ctx, _ := seedLeakFixture(t, g, mgr)

	// "other" is open: its global memories are ordinary global knowledge, and
	// proposing to pull them into one project would be a change nobody asked for.
	_, err := g.ProposeIsolationRelocations(ctx, "other")
	require.ErrorIs(t, err, ErrNotIsolated)

	_, err = g.ProposeIsolationRelocations(ctx, "never-registered")
	require.ErrorIs(t, err, ErrNotIsolated)

	pending, err := store.PendingProposals(ctx, g.db, store.ProposalRelocate)
	require.NoError(t, err)
	require.Empty(t, pending, "a refusal files nothing")
}

// A sealed project fences inbound writes from every other scope, which is
// exactly the check that must NOT apply here: the memory is coming home, and
// skipping the audit for sealed projects would strand the tightest fence with
// the leak it most needs repaired.
func TestProposeIsolationRelocations_SealedStillGetsItsLeaksBack(t *testing.T) {
	g, mgr, _ := newApplyFixture(t)
	ctx, leaked := seedLeakFixture(t, g, mgr)
	require.NoError(t, store.SetProjectIsolation(ctx, g.db, "acme", core.IsolationSealed))

	writable, err := store.CanWrite(ctx, g.db, "", "acme")
	require.NoError(t, err)
	require.False(t, writable, "the inbound fence is up -- the audit runs anyway")

	n, err := g.ProposeIsolationRelocations(ctx, "acme")
	require.NoError(t, err)
	require.Equal(t, len(leaked), n)
}

func TestApplyRelocate_MovesBehindTheFenceAndUndoes(t *testing.T) {
	g, mgr, _ := newApplyFixture(t)
	ctx, leaked := seedLeakFixture(t, g, mgr)
	mem := leaked[0]

	_, err := g.ProposeIsolationRelocations(ctx, "acme")
	require.NoError(t, err)
	pending, err := store.PendingProposals(ctx, g.db, store.ProposalRelocate)
	require.NoError(t, err)
	var target store.Proposal
	for _, p := range pending {
		if payloadString(p.Payload, "id") == mem.ID {
			target = p
		}
	}
	require.NotEmpty(t, target.ID)

	res, err := g.Apply(ctx, target.ID)
	require.NoError(t, err)
	require.Equal(t, "applied", res["status"])
	require.Equal(t, store.ProposalRelocate, res["kind"])
	require.Equal(t, "", res["from"])
	require.Equal(t, "acme", res["to"])

	moved, found, err := store.MemoryByID(ctx, g.db, mem.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "acme", moved.Project, "the memory now lives behind the fence")
	require.Nil(t, moved.InvalidAt, "a relocation keeps the memory active")
	require.Equal(t, mem.ID, moved.ID, "and keeps its identity")

	// The move is undoable -- it created nothing, so its inverse is faithful.
	require.True(t, CanUndoApply(store.ProposalRelocate))
	require.NoError(t, g.Undo(ctx, target.ID))

	back, found, err := store.MemoryByID(ctx, g.db, mem.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "", back.Project, "undo puts it back in the global scope")
	restored, _, err := store.ProposalByID(ctx, g.db, target.ID)
	require.NoError(t, err)
	require.Equal(t, store.ProposalPending, restored.Status, "and returns the question to the queue")

	// The undo is guarded, not blind: with the memory moved on by other means,
	// it refuses rather than sending it somewhere it never was.
	_, err = g.Apply(ctx, target.ID)
	require.NoError(t, err)
	full, err := g.loadActiveMemory(ctx, mem.ID)
	require.NoError(t, err)
	_, err = g.files.MoveMemory(ctx, full, "other")
	require.NoError(t, err)
	require.ErrorContains(t, g.Undo(ctx, target.ID), "has since moved out of acme")
	stuck, _, err := store.ProposalByID(ctx, g.db, target.ID)
	require.NoError(t, err)
	require.Equal(t, store.ProposalApplied, stuck.Status, "a refused undo leaves the world as the apply left it")
}
