package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// addPlanTask inserts an open task composed into plan, returning its id.
func addPlanTask(t *testing.T, db *sql.DB, project, plan, title string, seq int, deps ...string) string {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Add(time.Duration(seq) * time.Minute)
	require.NoError(t, CreateTask(context.Background(), db, core.Task{
		ID: id, ProjectSlug: project, Title: title, Status: core.TaskOpen,
		PlanSlug: plan, DependsOn: deps, CreatedAt: base, UpdatedAt: base,
	}))
	return id
}

// TestClaimTask_ConcurrentSingleWinner is the headline acceptance: many sessions
// race to claim the same ready task and exactly one wins; the rest see a conflict.
func TestClaimTask_ConcurrentSingleWinner(t *testing.T) {
	db := newTaskDB(t)
	id := addTask(t, db, "demo", "A", 1)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	const racers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		wins    int
		winner  string
		conflct int
	)
	start := make(chan struct{})
	for i := range racers {
		sess := "sess-" + strconv.Itoa(i)
		wg.Go(func() {
			<-start
			res, err := ClaimTask(context.Background(), db, id, sess, time.Minute, now)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
				winner = res.Task.ClaimedBy
			case errors.Is(err, ErrTaskClaimConflict):
				conflct++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()

	require.Equal(t, 1, wins, "exactly one claim must win the race")
	require.Equal(t, racers-1, conflct, "every loser must see a claim conflict")

	got, err := TaskByID(context.Background(), db, id)
	require.NoError(t, err)
	require.Equal(t, core.TaskInProgress, got.Status)
	require.Equal(t, winner, got.ClaimedBy)
}

func TestClaimTask_ConflictWhenHeld(t *testing.T) {
	db := newTaskDB(t)
	id := addTask(t, db, "demo", "A", 1)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	_, err := ClaimTask(context.Background(), db, id, "sessA", time.Minute, now)
	require.NoError(t, err)

	_, err = ClaimTask(context.Background(), db, id, "sessB", time.Minute, now.Add(time.Second))
	require.ErrorIs(t, err, ErrTaskClaimConflict)

	got, err := TaskByID(context.Background(), db, id)
	require.NoError(t, err)
	require.Equal(t, "sessA", got.ClaimedBy, "the original holder keeps the claim")
}

func TestClaimTask_HolderHeartbeatRefreshesLease(t *testing.T) {
	db := newTaskDB(t)
	id := addTask(t, db, "demo", "A", 1)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	first, err := ClaimTask(context.Background(), db, id, "sessA", time.Minute, now)
	require.NoError(t, err)
	require.NotNil(t, first.Task.LeaseExpiresAt)

	later := now.Add(30 * time.Second)
	second, err := ClaimTask(context.Background(), db, id, "sessA", time.Minute, later)
	require.NoError(t, err)
	require.False(t, second.Reclaimed, "re-claim by the holder is a heartbeat, not a reclaim")
	require.True(t, second.Task.LeaseExpiresAt.After(*first.Task.LeaseExpiresAt), "lease must extend")
}

func TestClaimTask_ExpiredLeaseReclaimable(t *testing.T) {
	db := newTaskDB(t)
	id := addTask(t, db, "demo", "A", 1)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	_, err := ClaimTask(context.Background(), db, id, "sessA", time.Minute, now)
	require.NoError(t, err)

	// sessB cannot steal while the lease is live.
	_, err = ClaimTask(context.Background(), db, id, "sessB", time.Minute, now.Add(30*time.Second))
	require.ErrorIs(t, err, ErrTaskClaimConflict)

	// After the lease lapses, sessB reclaims and the prior holder is recorded.
	res, err := ClaimTask(context.Background(), db, id, "sessB", time.Minute, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, res.Reclaimed)
	require.Equal(t, "sessA", res.PriorHolder)
	require.Equal(t, "sessB", res.Task.ClaimedBy)
}

func TestClaimTask_BlockedTaskNotClaimable(t *testing.T) {
	db := newTaskDB(t)
	a := addTask(t, db, "demo", "A", 1)
	b := addTask(t, db, "demo", "B", 2, a) // B blocked by open A
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	_, err := ClaimTask(context.Background(), db, b, "sessA", time.Minute, now)
	require.ErrorIs(t, err, ErrTaskClaimConflict, "a blocked task is not claimable")

	// Close the blocker; now B is claimable.
	setStatus(t, db, a, core.TaskDone)
	_, err = ClaimTask(context.Background(), db, b, "sessA", time.Minute, now.Add(time.Minute))
	require.NoError(t, err)
}

func TestReleaseTask_HolderReopensNonHolderRejected(t *testing.T) {
	db := newTaskDB(t)
	id := addTask(t, db, "demo", "A", 1)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	_, err := ClaimTask(context.Background(), db, id, "sessA", time.Minute, now)
	require.NoError(t, err)

	_, err = ReleaseTask(context.Background(), db, id, "sessB", now.Add(time.Second))
	require.ErrorIs(t, err, ErrTaskClaimConflict, "only the holder may release")

	released, err := ReleaseTask(context.Background(), db, id, "sessA", now.Add(2*time.Second))
	require.NoError(t, err)
	require.Equal(t, core.TaskOpen, released.Status)
	require.Empty(t, released.ClaimedBy)
	require.Nil(t, released.LeaseExpiresAt)

	// Reopened -> claimable again.
	require.Equal(t, []string{"A"}, readyTitles(t, db, "demo"))
}

func TestUpdateTask_CloseClearsClaim(t *testing.T) {
	db := newTaskDB(t)
	id := addTask(t, db, "demo", "A", 1)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	_, err := ClaimTask(context.Background(), db, id, "sessA", time.Minute, now)
	require.NoError(t, err)

	done := core.TaskDone
	updated, err := UpdateTask(context.Background(), db, id, TaskPatch{Status: &done}, "sessA", now.Add(time.Second))
	require.NoError(t, err)
	require.Empty(t, updated.ClaimedBy, "closing a task releases its claim")
	require.Nil(t, updated.LeaseExpiresAt)
}

// TestUpdateTask_HolderCheck is the acceptance for the write-lock: a live claim
// locks the task to its holder. A non-holder update is rejected; the holder
// updates freely; and once the lease lapses a non-holder may take over.
func TestUpdateTask_HolderCheck(t *testing.T) {
	db := newTaskDB(t)
	id := addTask(t, db, "demo", "A", 1)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	_, err := ClaimTask(context.Background(), db, id, "sessA", time.Minute, now)
	require.NoError(t, err)

	// A different session cannot mutate the live-claimed task.
	done := core.TaskDone
	_, err = UpdateTask(context.Background(), db, id, TaskPatch{Status: &done}, "sessB", now.Add(time.Second))
	require.ErrorIs(t, err, ErrTaskClaimConflict, "a non-holder cannot update a live-claimed task")

	// A caller with no session identity is likewise blocked.
	_, err = UpdateTask(context.Background(), db, id, TaskPatch{Status: &done}, "", now.Add(time.Second))
	require.ErrorIs(t, err, ErrTaskClaimConflict, "an empty actor is not the holder")

	// The holder still updates its own task.
	title := "A renamed"
	updated, err := UpdateTask(context.Background(), db, id, TaskPatch{Title: &title}, "sessA", now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, "A renamed", updated.Title)

	// After the lease expires the claim is no longer live, so a non-holder update
	// is allowed (matching ClaimTask's lazy-expiry reclaim).
	afterExpiry := now.Add(2 * time.Minute)
	reopened, err := UpdateTask(context.Background(), db, id, TaskPatch{Status: statusPtr(core.TaskOpen)}, "sessB", afterExpiry)
	require.NoError(t, err, "an expired lease no longer locks the task")
	require.Equal(t, core.TaskOpen, reopened.Status)
}

// TestForceReleaseTask covers the owner override: it reopens a claimed task
// regardless of holder or lease liveness, and errors when there is nothing to
// release.
func TestForceReleaseTask(t *testing.T) {
	db := newTaskDB(t)
	id := addTask(t, db, "demo", "A", 1)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	_, err := ClaimTask(context.Background(), db, id, "sessA", time.Minute, now)
	require.NoError(t, err)

	// Force-release while the lease is still live (the override's whole point).
	released, err := ForceReleaseTask(context.Background(), db, id, now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, core.TaskOpen, released.Status)
	require.Empty(t, released.ClaimedBy)
	require.Nil(t, released.LeaseExpiresAt)

	// Reopened -> a different session can now claim it.
	_, err = ClaimTask(context.Background(), db, id, "sessB", time.Minute, now.Add(2*time.Second))
	require.NoError(t, err)

	// Force-releasing a task that is not in progress is a conflict, not a no-op.
	other := addTask(t, db, "demo", "B", 2)
	_, err = ForceReleaseTask(context.Background(), db, other, now)
	require.ErrorIs(t, err, ErrTaskClaimConflict, "an unclaimed task has no lock to release")

	// An unknown id is a not-found.
	_, err = ForceReleaseTask(context.Background(), db, "nope", now)
	require.ErrorIs(t, err, ErrTaskNotFound)
}

func TestReleaseClaimsForSession(t *testing.T) {
	db := newTaskDB(t)
	a := addTask(t, db, "demo", "A", 1)
	b := addTask(t, db, "demo", "B", 2)
	c := addTask(t, db, "demo", "C", 3)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	_, err := ClaimTask(context.Background(), db, a, "sessA", time.Minute, now)
	require.NoError(t, err)
	_, err = ClaimTask(context.Background(), db, b, "sessA", time.Minute, now)
	require.NoError(t, err)
	_, err = ClaimTask(context.Background(), db, c, "sessB", time.Minute, now)
	require.NoError(t, err)

	n, err := ReleaseClaimsForSession(context.Background(), db, "sessA", now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, 2, n, "both of sessA's claims are released")

	// sessB's claim is untouched.
	got, err := TaskByID(context.Background(), db, c)
	require.NoError(t, err)
	require.Equal(t, "sessB", got.ClaimedBy)
	require.Equal(t, core.TaskInProgress, got.Status)
}

func TestPlanFilteredQueuesSeparatePlanFromDefault(t *testing.T) {
	db := newTaskDB(t)
	plain := addTask(t, db, "demo", "plain", 1)
	step := addPlanTask(t, db, "demo", "myplan", "step", 2)

	// Default ready-queue excludes plan steps.
	require.Equal(t, []string{"plain"}, readyTitles(t, db, "demo"))

	// Plan-scoped ready returns only that plan's steps.
	planReady, err := ReadyTasksForPlan(context.Background(), db, "demo", "myplan")
	require.NoError(t, err)
	require.Len(t, planReady, 1)
	require.Equal(t, step, planReady[0].ID)

	// Default list excludes plan steps; plan list includes only them.
	def, err := ListTasks(context.Background(), db, "demo", "")
	require.NoError(t, err)
	require.Len(t, def, 1)
	require.Equal(t, plain, def[0].ID)

	planList, err := ListTasksForPlan(context.Background(), db, "demo", "", "myplan")
	require.NoError(t, err)
	require.Len(t, planList, 1)
	require.Equal(t, step, planList[0].ID)
}

func TestActivePlansRollup(t *testing.T) {
	db := newTaskDB(t)
	// Plan "p1": 3 steps -- one done, one in flight (claimed), one claimable.
	s1 := addPlanTask(t, db, "demo", "p1", "s1", 1)
	s2 := addPlanTask(t, db, "demo", "p1", "s2", 2)
	addPlanTask(t, db, "demo", "p1", "s3", 3)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	setStatus(t, db, s1, core.TaskDone)
	_, err := ClaimTask(context.Background(), db, s2, "sessA", time.Minute, now)
	require.NoError(t, err)

	// Plan "p2": all steps closed -> omitted from active plans.
	d := addPlanTask(t, db, "demo", "p2", "done", 4)
	setStatus(t, db, d, core.TaskDone)

	plans, err := ActivePlans(context.Background(), db, "demo")
	require.NoError(t, err)
	require.Len(t, plans, 1, "only the incomplete plan is active")
	p := plans[0]
	require.Equal(t, "p1", p.Slug)
	require.Equal(t, 3, p.Total)
	require.Equal(t, 1, p.Done)
	require.Equal(t, 1, p.InFlight)
	require.Equal(t, 1, p.Claimable)
}
