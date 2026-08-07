package gardener

// Serialization of the apply paths that read a corpus file and write it back.
// The invariant under test is not "the apply worked" -- the other suites cover
// that -- but "the apply did not silently erase somebody else's write". That
// failure has no error to assert on: the losing rename simply vanished, and the
// index upsert is keyed by id, so even the UNIQUE file_path constraint stayed
// quiet. What it leaves behind is a file missing content, which is what these
// tests look for.

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/store"
)

// An archive appends a tombstone to the body it just read and rewrites the whole
// file from it, so it is the clearest read-modify-write in the apply surface --
// and the likeliest to be raced, since an agent appending findings to a memory
// the owner is retiring is ordinary rather than pathological.
func TestApplyArchive_SerializedAgainstConcurrentWriters(t *testing.T) {
	g, mgr, cx := newApplyFixture(t)
	ctx := cx()
	now := time.Now().UTC()

	writeMem(t, mgr, "stale-thing", "", "1", core.KindGotcha, now, "original body")
	idx, found, err := store.MemoryByName(ctx, g.db, "", "stale-thing")
	require.NoError(t, err)
	require.True(t, found)

	p, err := store.CreateProposal(ctx, g.db, store.ProposalArchive, map[string]any{
		"key": "staleness:" + idx.ID, "id": idx.ID, "name": idx.Name,
	})
	require.NoError(t, err)

	relPath := files.MemoryRelPath("", "stale-thing")
	const appenders = 6
	appendErrs := make([]error, appenders)
	var applyErr error

	var wg sync.WaitGroup
	wg.Add(appenders + 1)
	go func() {
		defer wg.Done()
		_, applyErr = g.Apply(ctx, p.ID)
	}()
	for i := range appenders {
		go func() {
			defer wg.Done()
			_, appendErrs[i] = mgr.MutateMemory(ctx, relPath, func(_ context.Context, mem core.Memory) (core.Memory, error) {
				mem.Body += "\nmarker-" + strconv.Itoa(i)
				return mem, nil
			})
		}()
	}
	wg.Wait()

	require.NoError(t, applyErr)
	for i, aerr := range appendErrs {
		require.NoError(t, aerr, "appender %d", i)
	}

	onDisk, err := mgr.Store().ReadMemory(relPath)
	require.NoError(t, err)
	for i := range appenders {
		require.Contains(t, onDisk.Body, "marker-"+strconv.Itoa(i),
			"the archive rewrote the whole file and dropped a concurrent append")
	}
	require.NotNil(t, onDisk.InvalidAt, "the archive itself still landed")
}

// The note half of the same shape: settling a stale plan retags and re-describes
// the cc-plan note, rewriting the file the capture hook writes on every plan
// save. The status guard is part of the read, so it has to be inside the lock
// too -- otherwise the apply judges tags that have already been replaced.
func TestApplySettlePlan_SerializedAgainstConcurrentWriters(t *testing.T) {
	g, mgr, cx := newApplyFixture(t)
	ctx := cx()
	now := time.Now().UTC()

	note := writePlanNote(t, mgr, "cc-plan-raced", "raced-plan", "Raced Plan", plans.StatusPresented, now)
	p, err := store.CreateProposal(ctx, g.db, store.ProposalAbandonPlan, map[string]any{
		"key": "abandon_plan:" + note.ID, "id": note.ID, "plan_status": plans.StatusPresented,
	})
	require.NoError(t, err)

	relPath := files.NoteRelPath("demo", "cc-plan-raced")
	const appenders = 6
	appendErrs := make([]error, appenders)
	var applyErr error

	var wg sync.WaitGroup
	wg.Add(appenders + 1)
	go func() {
		defer wg.Done()
		_, applyErr = g.Apply(ctx, p.ID)
	}()
	for i := range appenders {
		go func() {
			defer wg.Done()
			_, appendErrs[i] = mgr.MutateNote(ctx, relPath, func(_ context.Context, n core.Note) (core.Note, error) {
				n.Body += "\nmarker-" + strconv.Itoa(i)
				return n, nil
			})
		}()
	}
	wg.Wait()

	require.NoError(t, applyErr)
	for i, aerr := range appendErrs {
		require.NoError(t, aerr, "appender %d", i)
	}

	onDisk, err := mgr.Store().ReadNote(relPath)
	require.NoError(t, err)
	for i := range appenders {
		require.Contains(t, onDisk.Body, "marker-"+strconv.Itoa(i),
			"the settlement rewrote the whole file and dropped a concurrent append")
	}
	require.Equal(t, plans.StatusAbandoned, plans.StatusFromTags(onDisk.Tags), "the settlement itself still landed")
}
