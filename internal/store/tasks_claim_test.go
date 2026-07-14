package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
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

// TestClaimTask_StaleClaimOnOpenTaskSelfHeals is the regression for legacy rows
// stuck from before UpdateTask cleared claim fields on reopen: an open task with
// a leftover claimed_by (and even a still-future lease) must be claimable, since
// a live claim only exists on an in_progress task. Claiming it overwrites the
// residue and is an ordinary claim, not a reclaim.
func TestClaimTask_StaleClaimOnOpenTaskSelfHeals(t *testing.T) {
	db := newTaskDB(t)
	id := addTask(t, db, "demo", "A", 1)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	// Manufacture the legacy stuck state directly: no current code path writes
	// status='open' with a claim. The lease is deliberately in the future to
	// prove that status, not lease expiry, gates the open branch.
	_, err := db.Exec(`UPDATE tasks SET claimed_by = 'deadsess', lease_expires_at = ? WHERE id = ?`,
		core.FormatTime(now.Add(time.Hour)), id)
	require.NoError(t, err)
	require.Equal(t, []string{"A"}, readyTitles(t, db, "demo"), "the stuck row still shows as ready")

	res, err := ClaimTask(context.Background(), db, id, "sessB", time.Minute, now)
	require.NoError(t, err, "an open task must be claimable regardless of a stale claimed_by")
	require.Equal(t, "sessB", res.Task.ClaimedBy)
	require.Equal(t, core.TaskInProgress, res.Task.Status)
	require.False(t, res.Reclaimed, "overwriting stale residue on an open row is not a lease steal")

	// The healed claim is a normal live claim: a third session cannot steal it.
	_, err = ClaimTask(context.Background(), db, id, "sessC", time.Minute, now.Add(time.Second))
	require.ErrorIs(t, err, ErrTaskClaimConflict, "a genuinely held in_progress task stays unstealable")

	got, err := TaskByID(context.Background(), db, id)
	require.NoError(t, err)
	require.Equal(t, "sessB", got.ClaimedBy)
}

// TestClaimTask_StaleClaimDoesNotBypassReadiness: the relaxed open branch still
// enforces the readiness rule -- a blocked open task is unclaimable even when it
// carries a stale claim.
func TestClaimTask_StaleClaimDoesNotBypassReadiness(t *testing.T) {
	db := newTaskDB(t)
	a := addTask(t, db, "demo", "A", 1)
	b := addTask(t, db, "demo", "B", 2, a) // B blocked by open A
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	_, err := db.Exec(`UPDATE tasks SET claimed_by = 'deadsess' WHERE id = ?`, b)
	require.NoError(t, err)

	_, err = ClaimTask(context.Background(), db, b, "sessA", time.Minute, now)
	require.ErrorIs(t, err, ErrTaskClaimConflict, "a blocked task is not claimable, stale claim or not")
}

// TestMigrationOpenClaimsRepair: migration 007 clears claim fields on legacy
// status='open' rows (stuck from before reopen released claims) while leaving a
// genuinely held in_progress row untouched, and its UPDATE is idempotent.
func TestMigrationOpenClaimsRepair(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	// Build the schema as it was before the repair migration, so the legacy rows
	// pre-exist it the way they will on a live database at the next restart.
	var pre []Migration
	for _, m := range migrationList() {
		if m.Version < 7 {
			pre = append(pre, m)
		}
	}
	require.NoError(t, migrate(db, pre))

	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	insert := func(id, status, claimedBy string, lease any) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO tasks (id, project_slug, title, status,
			claimed_by, lease_expires_at, created_at, updated_at)
			VALUES (?, 'demo', ?, ?, ?, ?, ?, ?)`,
			id, id, status, claimedBy, lease, core.FormatTime(now), core.FormatTime(now))
		require.NoError(t, err)
	}
	insert("stuck-claim", "open", "deadsess", core.FormatTime(now.Add(time.Hour)))
	insert("stuck-lease", "open", "", core.FormatTime(now.Add(time.Hour)))
	insert("held", "in_progress", "sessA", core.FormatTime(now.Add(time.Hour)))

	require.NoError(t, migrate(db, migrationList()))

	assertRepaired := func() {
		t.Helper()
		for _, id := range []string{"stuck-claim", "stuck-lease"} {
			got, err := TaskByID(context.Background(), db, id)
			require.NoError(t, err)
			require.Equal(t, core.TaskOpen, got.Status)
			require.Empty(t, got.ClaimedBy, "%s: repair must clear the stale claim", id)
			require.Nil(t, got.LeaseExpiresAt, "%s: repair must clear the stale lease", id)
		}
		held, err := TaskByID(context.Background(), db, "held")
		require.NoError(t, err)
		require.Equal(t, core.TaskInProgress, held.Status)
		require.Equal(t, "sessA", held.ClaimedBy, "a live in_progress claim survives the repair")
		require.NotNil(t, held.LeaseExpiresAt)
	}
	assertRepaired()

	// The repair UPDATE is idempotent: re-running it is a no-op.
	_, err = db.Exec(migration007)
	require.NoError(t, err)
	assertRepaired()

	// End to end: the previously stuck row is claimable after the repair.
	res, err := ClaimTask(context.Background(), db, "stuck-claim", "sessB", time.Minute, now)
	require.NoError(t, err)
	require.Equal(t, "sessB", res.Task.ClaimedBy)
	require.False(t, res.Reclaimed)
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

// TestUpdateTask_ReopenClearsClaim is the regression for a reopened task
// keeping a stale claim: before the fix, moving an in_progress task back to
// open via UpdateTask left claimed_by/lease_expires_at behind, so the task
// showed in the ready queue but was permanently unclaimable (ClaimTask requires
// an empty claimed_by on open tasks, and both release paths require in_progress).
func TestUpdateTask_ReopenClearsClaim(t *testing.T) {
	db := newTaskDB(t)
	id := addTask(t, db, "demo", "A", 1)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	_, err := ClaimTask(context.Background(), db, id, "sessA", time.Minute, now)
	require.NoError(t, err)

	// After the lease lapses, a non-holder reopens the task via UpdateTask.
	reopened, err := UpdateTask(context.Background(), db, id,
		TaskPatch{Status: statusPtr(core.TaskOpen)}, "sessB", now.Add(2*time.Minute))
	require.NoError(t, err)
	require.Equal(t, core.TaskOpen, reopened.Status)
	require.Empty(t, reopened.ClaimedBy, "reopening must release the stale claim")
	require.Nil(t, reopened.LeaseExpiresAt)

	// The reopened task is both ready and actually claimable again.
	require.Equal(t, []string{"A"}, readyTitles(t, db, "demo"))
	res, err := ClaimTask(context.Background(), db, id, "sessB", time.Minute, now.Add(3*time.Minute))
	require.NoError(t, err)
	require.Equal(t, "sessB", res.Task.ClaimedBy)
}

// TestUpdateTask_HolderReopenReleasesClaim: the holder reopening its own
// live-claimed task releases the claim, matching ReleaseTask semantics.
func TestUpdateTask_HolderReopenReleasesClaim(t *testing.T) {
	db := newTaskDB(t)
	id := addTask(t, db, "demo", "A", 1)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	_, err := ClaimTask(context.Background(), db, id, "sessA", time.Minute, now)
	require.NoError(t, err)

	reopened, err := UpdateTask(context.Background(), db, id,
		TaskPatch{Status: statusPtr(core.TaskOpen)}, "sessA", now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, core.TaskOpen, reopened.Status)
	require.Empty(t, reopened.ClaimedBy)
	require.Nil(t, reopened.LeaseExpiresAt)

	// Another session can claim it immediately.
	_, err = ClaimTask(context.Background(), db, id, "sessB", time.Minute, now.Add(2*time.Second))
	require.NoError(t, err)
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
	// Plan "p1": 4 steps -- one done, one in flight (claimed), one claimable,
	// and one open but blocked by the claimable step (so not claimable).
	s1 := addPlanTask(t, db, "demo", "p1", "s1", 1)
	s2 := addPlanTask(t, db, "demo", "p1", "s2", 2)
	s3 := addPlanTask(t, db, "demo", "p1", "s3", 3)
	addPlanTask(t, db, "demo", "p1", "s4", 4, s3)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	setStatus(t, db, s1, core.TaskDone)
	_, err := ClaimTask(context.Background(), db, s2, "sessA", time.Minute, now)
	require.NoError(t, err)

	// Plan "p2": all steps closed -> omitted from active plans.
	d := addPlanTask(t, db, "demo", "p2", "done", 5)
	setStatus(t, db, d, core.TaskDone)

	plans, err := ActivePlans(context.Background(), db, "demo")
	require.NoError(t, err)
	require.Len(t, plans, 1, "only the incomplete plan is active")
	p := plans[0]
	require.Equal(t, "p1", p.Slug)
	require.Equal(t, 4, p.Total)
	require.Equal(t, 1, p.Done)
	require.Equal(t, 1, p.InFlight)
	require.Equal(t, 1, p.Claimable, "a blocked open step is not claimable")
}
