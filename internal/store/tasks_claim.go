// Lease-based task claiming: the compare-and-set claim and the four ways a claim
// ends (holder release, owner force-release, session teardown, lease expiry).
// Expiry is enforced lazily inside ClaimTask -- there is no background sweeper.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// ErrTaskClaimConflict is returned when a claim loses the compare-and-set race:
// the task is already held by another live lease, or is not in a claimable state.
var ErrTaskClaimConflict = errors.New("task already claimed")

// ClaimResult reports the outcome of a successful ClaimTask. Reclaimed is true
// when a different session's lapsed lease was stolen; PriorHolder then names that
// session so the caller can record a reclaim event.
type ClaimResult struct {
	Task        core.Task
	Reclaimed   bool
	PriorHolder string
}

// ClaimTask atomically claims a task for sessionID, moving it to in_progress and
// stamping a lease that expires at now+lease. It is a compare-and-set (mirrors
// ResolveProposal): the write lands only when the task is claimable --
//
//	(a) open and ready (no open/in_progress blocker), or
//	(b) already held by sessionID (a re-claim / heartbeat that refreshes the lease), or
//	(c) in_progress with an expired lease (a steal from a dead holder).
//
// Otherwise it returns ErrTaskClaimConflict. Lease expiry is enforced lazily
// here -- there is no background sweeper. sessionID must be non-empty.
//
// Branch (a) deliberately ignores claimed_by: a live claim exists only on an
// in_progress task with an unexpired lease (core.Task.ClaimLive), and every
// path that writes status='open' clears the claim fields, so a claim value on
// an open row can only be stale residue (rows written before reopen released
// claims). Claiming such a row overwrites the residue, self-healing it.
func ClaimTask(ctx context.Context, db *sql.DB, id, sessionID string, lease time.Duration, now time.Time) (ClaimResult, error) {
	if sessionID == "" {
		return ClaimResult{}, errors.New("store.ClaimTask: empty session id")
	}
	prior, ok, err := taskByIDTx(ctx, db, id)
	if err != nil {
		return ClaimResult{}, err
	}
	if !ok {
		return ClaimResult{}, fmt.Errorf("store.ClaimTask: %w: %q", ErrTaskNotFound, id)
	}

	nowStr := core.FormatTime(now.UTC())
	expStr := core.FormatTime(now.Add(lease).UTC())
	res, err := db.ExecContext(ctx, `
		UPDATE tasks
		   SET status = 'in_progress', claimed_by = ?, lease_expires_at = ?, updated_at = ?
		 WHERE id = ?
		   AND (
		         (status = 'open' AND NOT EXISTS (
		             SELECT 1 FROM task_deps d
		             JOIN tasks b ON b.id = d.depends_on
		             WHERE d.task_id = tasks.id AND b.status IN ('open','in_progress')))
		      OR (status = 'in_progress' AND (
		             claimed_by = ?
		          OR (lease_expires_at IS NOT NULL AND lease_expires_at < ?)))
		   )`,
		sessionID, expStr, nowStr, id, sessionID, nowStr)
	if err != nil {
		return ClaimResult{}, fmt.Errorf("store.ClaimTask: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ClaimResult{Task: prior}, fmt.Errorf("store.ClaimTask: %w: task %q held by %q",
			ErrTaskClaimConflict, id, prior.ClaimedBy)
	}
	updated, err := TaskByID(ctx, db, id)
	if err != nil {
		return ClaimResult{}, err
	}
	// Only a steal from an in_progress holder counts as a reclaim; overwriting a
	// stale claim left on an open row (branch (a)) is an ordinary claim of a
	// ready task, not a lease steal.
	reclaimed := prior.Status == core.TaskInProgress &&
		prior.ClaimedBy != "" && prior.ClaimedBy != sessionID
	holder := ""
	if reclaimed {
		holder = prior.ClaimedBy
	}
	return ClaimResult{Task: updated, Reclaimed: reclaimed, PriorHolder: holder}, nil
}

// ReleaseTask releases a task held by sessionID, reopening it (status back to
// open, claim and lease cleared) so another agent can pick it up. Only the
// current holder may release; otherwise it returns ErrTaskClaimConflict (or
// ErrTaskNotFound when the id is unknown).
func ReleaseTask(ctx context.Context, db *sql.DB, id, sessionID string, now time.Time) (core.Task, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE tasks SET status = 'open', claimed_by = '', lease_expires_at = NULL, updated_at = ?
		 WHERE id = ? AND claimed_by = ? AND status = 'in_progress'`,
		core.FormatTime(now.UTC()), id, sessionID)
	if err != nil {
		return core.Task{}, fmt.Errorf("store.ReleaseTask: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, ok, err := taskByIDTx(ctx, db, id); err != nil {
			return core.Task{}, err
		} else if !ok {
			return core.Task{}, fmt.Errorf("store.ReleaseTask: %w: %q", ErrTaskNotFound, id)
		}
		return core.Task{}, fmt.Errorf("store.ReleaseTask: %w: task %q not held by %q",
			ErrTaskClaimConflict, id, sessionID)
	}
	return TaskByID(ctx, db, id)
}

// ForceReleaseTask unconditionally releases a claimed (in_progress) task,
// reopening it (status back to open, claim and lease cleared) regardless of who
// holds it or whether the lease is still live. It backs the owner override (the
// console "release lock" button and `seam task release --force`); it is not
// reachable from the agent MCP surface. Unlike ReleaseTask it does not check the
// holder. Returns ErrTaskClaimConflict when the task is not in_progress (nothing
// to release) and ErrTaskNotFound when the id is unknown.
func ForceReleaseTask(ctx context.Context, db *sql.DB, id string, now time.Time) (core.Task, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE tasks SET status = 'open', claimed_by = '', lease_expires_at = NULL, updated_at = ?
		 WHERE id = ? AND status = 'in_progress'`,
		core.FormatTime(now.UTC()), id)
	if err != nil {
		return core.Task{}, fmt.Errorf("store.ForceReleaseTask: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, ok, err := taskByIDTx(ctx, db, id); err != nil {
			return core.Task{}, err
		} else if !ok {
			return core.Task{}, fmt.Errorf("store.ForceReleaseTask: %w: %q", ErrTaskNotFound, id)
		}
		return core.Task{}, fmt.Errorf("store.ForceReleaseTask: %w: task %q is not claimed",
			ErrTaskClaimConflict, id)
	}
	return TaskByID(ctx, db, id)
}

// ReleaseClaimsForSession reopens every in_progress task currently claimed by
// sessionID (called on session_end so a departing agent's work returns to the
// queue). It returns the number of tasks released.
func ReleaseClaimsForSession(ctx context.Context, db *sql.DB, sessionID string, now time.Time) (int, error) {
	if sessionID == "" {
		return 0, nil
	}
	res, err := db.ExecContext(ctx, `
		UPDATE tasks SET status = 'open', claimed_by = '', lease_expires_at = NULL, updated_at = ?
		 WHERE claimed_by = ? AND status = 'in_progress'`,
		core.FormatTime(now.UTC()), sessionID)
	if err != nil {
		return 0, fmt.Errorf("store.ReleaseClaimsForSession: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
