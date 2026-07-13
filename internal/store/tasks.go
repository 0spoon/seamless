package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// taskCols is the SELECT list for the tasks table, matching scanTask.
const taskCols = `id, project_slug, title, body, status, created_by,
	plan_slug, claimed_by, lease_expires_at, created_at, updated_at, closed_at`

// cycleWalkCap bounds the dependency walk in createsCycle, a backstop against a
// pathological graph (the edges form a DAG by construction, so this is never hit
// in practice).
const cycleWalkCap = 10000

// ErrTaskCycle is returned when adding a dependency edge would create a cycle.
var ErrTaskCycle = errors.New("dependency would create a cycle")

// ErrTaskNotFound is returned when a task id does not exist (e.g. a dangling
// depends_on reference).
var ErrTaskNotFound = errors.New("task not found")

// ErrTaskClaimConflict is returned when a claim loses the compare-and-set race:
// the task is already held by another live lease, or is not in a claimable state.
var ErrTaskClaimConflict = errors.New("task already claimed")

// rowQuerier is the read subset shared by *sql.DB and *sql.Tx, so the cycle and
// dangling-reference checks run identically inside or outside a transaction.
type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// TaskPatch is the set of mutable fields UpdateTask may change; nil fields are
// left untouched. AddDependsOn edges are added (existing edges are kept).
// ProjectSlug reassigns the task to another project (used when a split moves a
// project's open work to a child); the holder-lock still applies.
type TaskPatch struct {
	Status       *core.TaskStatus
	Title        *string
	Body         *string
	ProjectSlug  *string
	AddDependsOn []string
}

// CreateTask inserts a task and its dependency edges in one transaction. Every
// depends_on must reference an existing task (dangling references are rejected)
// and must not create a cycle. The caller mints the ULID id and timestamps.
func CreateTask(ctx context.Context, db *sql.DB, t core.Task) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store.CreateTask: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var closedAt any
	if t.ClosedAt != nil {
		closedAt = core.FormatTime(*t.ClosedAt)
	}
	var leaseAt any
	if t.LeaseExpiresAt != nil {
		leaseAt = core.FormatTime(*t.LeaseExpiresAt)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tasks (id, project_slug, title, body, status, created_by,
		                   plan_slug, claimed_by, lease_expires_at,
		                   created_at, updated_at, closed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ProjectSlug, t.Title, t.Body, string(t.Status), t.CreatedBy,
		t.PlanSlug, t.ClaimedBy, leaseAt,
		core.FormatTime(t.CreatedAt), core.FormatTime(t.UpdatedAt), closedAt); err != nil {
		return fmt.Errorf("store.CreateTask: insert: %w", err)
	}
	if err := addDeps(ctx, tx, t.ID, t.DependsOn); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store.CreateTask: commit: %w", err)
	}
	return nil
}

// UpdateTask applies patch to the task and returns the updated task (with deps).
// Moving to a terminal status stamps closed_at; reopening clears it. Added deps
// are validated for existence and cycles like CreateTask.
//
// actor is the caller's session id (empty for callers with no session). A task
// with a live claim (see core.Task.ClaimLive) is locked to its holder: any
// mutation by a different actor is rejected with ErrTaskClaimConflict, so an
// agent's claimed task cannot be edited or closed out from under it. The holder
// itself, an expired lease, and the owner override (ForceReleaseTask) are the
// only ways past the lock.
func UpdateTask(ctx context.Context, db *sql.DB, id string, patch TaskPatch, actor string, now time.Time) (core.Task, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return core.Task{}, fmt.Errorf("store.UpdateTask: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	t, ok, err := taskByIDTx(ctx, tx, id)
	if err != nil {
		return core.Task{}, err
	}
	if !ok {
		return core.Task{}, fmt.Errorf("store.UpdateTask: %w: %q", ErrTaskNotFound, id)
	}
	if t.ClaimLive(now) && t.ClaimedBy != actor {
		return core.Task{}, fmt.Errorf("store.UpdateTask: %w: task %q held by %q",
			ErrTaskClaimConflict, id, t.ClaimedBy)
	}

	if patch.Title != nil {
		t.Title = *patch.Title
	}
	if patch.Body != nil {
		t.Body = *patch.Body
	}
	if patch.ProjectSlug != nil {
		t.ProjectSlug = *patch.ProjectSlug
	}
	if patch.Status != nil && *patch.Status != t.Status {
		t.Status = *patch.Status
		if t.Status.Closed() {
			at := now.UTC()
			t.ClosedAt = &at
			// A finished task releases its claim so it never lingers as a
			// stale holder or blocks a lease reclaim.
			t.ClaimedBy = ""
			t.LeaseExpiresAt = nil
		} else {
			t.ClosedAt = nil
		}
	}
	t.UpdatedAt = now.UTC()

	var closedAt any
	if t.ClosedAt != nil {
		closedAt = core.FormatTime(*t.ClosedAt)
	}
	var leaseAt any
	if t.LeaseExpiresAt != nil {
		leaseAt = core.FormatTime(*t.LeaseExpiresAt)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE tasks SET title = ?, body = ?, project_slug = ?, status = ?, updated_at = ?, closed_at = ?,
		                 claimed_by = ?, lease_expires_at = ?
		 WHERE id = ?`,
		t.Title, t.Body, t.ProjectSlug, string(t.Status), core.FormatTime(t.UpdatedAt), closedAt,
		t.ClaimedBy, leaseAt, t.ID); err != nil {
		return core.Task{}, fmt.Errorf("store.UpdateTask: update: %w", err)
	}
	if err := addDeps(ctx, tx, t.ID, patch.AddDependsOn); err != nil {
		return core.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Task{}, fmt.Errorf("store.UpdateTask: commit: %w", err)
	}
	return TaskByID(ctx, db, id)
}

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
//	(a) open, unclaimed, and ready (no open/in_progress blocker), or
//	(b) already held by sessionID (a re-claim / heartbeat that refreshes the lease), or
//	(c) in_progress with an expired lease (a steal from a dead holder).
//
// Otherwise it returns ErrTaskClaimConflict. Lease expiry is enforced lazily
// here -- there is no background sweeper. sessionID must be non-empty.
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
		         (status = 'open' AND claimed_by = '' AND NOT EXISTS (
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
	reclaimed := prior.ClaimedBy != "" && prior.ClaimedBy != sessionID
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

// PlanRollup is the per-plan aggregate the briefing surfaces: Total step tasks,
// how many are Done (closed), InFlight (in_progress), and Claimable (ready).
type PlanRollup struct {
	Slug      string `json:"slug"`
	Total     int    `json:"total"`
	Done      int    `json:"done"`
	InFlight  int    `json:"inFlight"`
	Claimable int    `json:"claimable"`
}

// ActivePlans returns a rollup for each not-yet-complete plan in a project
// (plans whose every step is closed are omitted, like a done stage). Claimable
// counts ready open steps; the plan's status is derived, never stored.
func ActivePlans(ctx context.Context, db *sql.DB, project string) ([]PlanRollup, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT plan_slug,
		       COUNT(*),
		       SUM(CASE WHEN status IN ('done','dropped') THEN 1 ELSE 0 END),
		       SUM(CASE WHEN status = 'in_progress' THEN 1 ELSE 0 END)
		  FROM tasks
		 WHERE project_slug = ? AND plan_slug <> ''
		 GROUP BY plan_slug
		 ORDER BY plan_slug ASC`, project)
	if err != nil {
		return nil, fmt.Errorf("store.ActivePlans: %w", err)
	}
	var plans []PlanRollup
	func() {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var p PlanRollup
			if err = rows.Scan(&p.Slug, &p.Total, &p.Done, &p.InFlight); err != nil {
				return
			}
			if p.Done >= p.Total {
				continue // every step closed -> plan complete, not active
			}
			plans = append(plans, p)
		}
		err = rows.Err()
	}()
	if err != nil {
		return nil, fmt.Errorf("store.ActivePlans: %w", err)
	}
	for i := range plans {
		ready, err := ReadyTasksForPlan(ctx, db, project, plans[i].Slug)
		if err != nil {
			return nil, err
		}
		plans[i].Claimable = len(ready)
	}
	return plans, nil
}

// addDeps inserts depends-on edges for taskID, rejecting dangling references and
// cycles. Duplicate edges are ignored. It runs inside the caller's transaction.
func addDeps(ctx context.Context, tx *sql.Tx, taskID string, deps []string) error {
	for _, dep := range dedupeStrings(deps) {
		if dep == taskID {
			return fmt.Errorf("store: task %q cannot depend on itself", taskID)
		}
		exists, err := taskExists(ctx, tx, dep)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("store: %w: depends_on %q", ErrTaskNotFound, dep)
		}
		cyclic, err := createsCycle(ctx, tx, taskID, dep)
		if err != nil {
			return err
		}
		if cyclic {
			return fmt.Errorf("store: %w: %q -> %q", ErrTaskCycle, taskID, dep)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO task_deps (task_id, depends_on) VALUES (?, ?)`,
			taskID, dep); err != nil {
			return fmt.Errorf("store: insert dep: %w", err)
		}
	}
	return nil
}

// createsCycle reports whether adding the edge taskID -> dep (taskID is blocked
// by dep) would close a cycle: it walks dep's existing depends-on chain and
// returns true if it can already reach taskID.
func createsCycle(ctx context.Context, q rowQuerier, taskID, dep string) (bool, error) {
	stack := []string{dep}
	visited := make(map[string]bool)
	for steps := 0; len(stack) > 0 && steps < cycleWalkCap; steps++ {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == taskID {
			return true, nil
		}
		if visited[cur] {
			continue
		}
		visited[cur] = true
		children, err := dependsOnOf(ctx, q, cur)
		if err != nil {
			return false, err
		}
		stack = append(stack, children...)
	}
	return false, nil
}

// dependsOnOf returns the ids a task directly depends on.
func dependsOnOf(ctx context.Context, q rowQuerier, taskID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT depends_on FROM task_deps WHERE task_id = ?`, taskID)
	if err != nil {
		return nil, fmt.Errorf("store: dependsOnOf: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: dependsOnOf scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func taskExists(ctx context.Context, q rowQuerier, id string) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT 1 FROM tasks WHERE id = ? LIMIT 1`, id)
	if err != nil {
		return false, fmt.Errorf("store: taskExists: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return rows.Next(), rows.Err()
}

// ReadyTasks returns the actionable tasks for a project: open tasks with no
// blocking dependency still open or in_progress (done/dropped deps do not block).
// in_progress tasks are excluded (already claimed) but still block their
// dependents. Ordered oldest-created first, ties broken by id (ULID-monotonic),
// so the queue is stable and agent-predictable.
func ReadyTasks(ctx context.Context, db *sql.DB, project string) ([]core.Task, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks t
		WHERE t.status = 'open' AND t.project_slug = ? AND t.plan_slug = ''
		  AND NOT EXISTS (
		      SELECT 1 FROM task_deps d
		      JOIN tasks b ON b.id = d.depends_on
		      WHERE d.task_id = t.id AND b.status IN ('open','in_progress'))
		ORDER BY t.created_at ASC, t.id ASC`, project)
	if err != nil {
		return nil, fmt.Errorf("store.ReadyTasks: %w", err)
	}
	return scanTasksWithDeps(ctx, db, rows)
}

// ReadyTasksForPlan returns the ready (claimable) step tasks of one plan in a
// project: open, unclaimed, with no unfinished blocker. Same readiness rule as
// ReadyTasks, scoped to plan_slug instead of excluding plan tasks.
func ReadyTasksForPlan(ctx context.Context, db *sql.DB, project, plan string) ([]core.Task, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks t
		WHERE t.status = 'open' AND t.project_slug = ? AND t.plan_slug = ?
		  AND NOT EXISTS (
		      SELECT 1 FROM task_deps d
		      JOIN tasks b ON b.id = d.depends_on
		      WHERE d.task_id = t.id AND b.status IN ('open','in_progress'))
		ORDER BY t.created_at ASC, t.id ASC`, project, plan)
	if err != nil {
		return nil, fmt.Errorf("store.ReadyTasksForPlan: %w", err)
	}
	return scanTasksWithDeps(ctx, db, rows)
}

// AllReadyTasks returns the ready tasks across every project (see ReadyTasks for
// the readiness rule), oldest-created first. It backs the console Tasks page.
func AllReadyTasks(ctx context.Context, db *sql.DB) ([]core.Task, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks t
		WHERE t.status = 'open'
		  AND NOT EXISTS (
		      SELECT 1 FROM task_deps d
		      JOIN tasks b ON b.id = d.depends_on
		      WHERE d.task_id = t.id AND b.status IN ('open','in_progress'))
		ORDER BY t.created_at ASC, t.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store.AllReadyTasks: %w", err)
	}
	return scanTasksWithDeps(ctx, db, rows)
}

// AllBlockedTasks returns open-but-not-ready tasks across every project, each
// with its still-open blockers.
func AllBlockedTasks(ctx context.Context, db *sql.DB) ([]BlockedTask, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks t
		WHERE t.status = 'open'
		  AND EXISTS (
		      SELECT 1 FROM task_deps d
		      JOIN tasks b ON b.id = d.depends_on
		      WHERE d.task_id = t.id AND b.status IN ('open','in_progress'))
		ORDER BY t.created_at ASC, t.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store.AllBlockedTasks: %w", err)
	}
	tasks, err := scanTasksWithDeps(ctx, db, rows)
	if err != nil {
		return nil, err
	}
	out := make([]BlockedTask, 0, len(tasks))
	for _, t := range tasks {
		blockers, err := openBlockers(ctx, db, t.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, BlockedTask{Task: t, Blockers: blockers})
	}
	return out, nil
}

// AllTasksByStatus returns every task with the given status across all projects,
// newest-updated first.
func AllTasksByStatus(ctx context.Context, db *sql.DB, status core.TaskStatus) ([]core.Task, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks
		WHERE status = ? ORDER BY updated_at DESC, id DESC`, string(status))
	if err != nil {
		return nil, fmt.Errorf("store.AllTasksByStatus: %w", err)
	}
	return scanTasksWithDeps(ctx, db, rows)
}

// ListTasks returns a project's tasks, optionally filtered by status, newest
// first. An empty status returns every status.
func ListTasks(ctx context.Context, db *sql.DB, project string, status core.TaskStatus) ([]core.Task, error) {
	query := `SELECT ` + taskCols + ` FROM tasks WHERE project_slug = ? AND plan_slug = ''`
	args := []any{project}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, string(status))
	}
	query += ` ORDER BY created_at DESC, id DESC`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store.ListTasks: %w", err)
	}
	return scanTasksWithDeps(ctx, db, rows)
}

// ListTasksForPlan returns a plan's step tasks in a project, optionally filtered
// by status, newest first. Unlike ListTasks it includes (only) plan-scoped tasks.
func ListTasksForPlan(ctx context.Context, db *sql.DB, project string, status core.TaskStatus, plan string) ([]core.Task, error) {
	query := `SELECT ` + taskCols + ` FROM tasks WHERE project_slug = ? AND plan_slug = ?`
	args := []any{project, plan}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, string(status))
	}
	query += ` ORDER BY created_at DESC, id DESC`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store.ListTasksForPlan: %w", err)
	}
	return scanTasksWithDeps(ctx, db, rows)
}

// BlockedTask is an open task that is not ready, paired with the blockers still
// keeping it off the ready queue (open/in_progress dependencies).
type BlockedTask struct {
	Task     core.Task   `json:"task"`
	Blockers []core.Task `json:"blockers"`
}

// BlockedTasks returns a project's open-but-not-ready tasks, each with its
// still-open blockers, so a caller can render the dependency chain legibly.
func BlockedTasks(ctx context.Context, db *sql.DB, project string) ([]BlockedTask, error) {
	return blockedTasks(ctx, db, project, false, "")
}

// BlockedTasksForPlan returns a plan's open-but-not-ready step tasks, each with
// its still-open blockers.
func BlockedTasksForPlan(ctx context.Context, db *sql.DB, project, plan string) ([]BlockedTask, error) {
	return blockedTasks(ctx, db, project, true, plan)
}

// blockedTasks backs BlockedTasks (byPlan=false, non-plan tasks only) and
// BlockedTasksForPlan (byPlan=true, only the given plan's tasks).
func blockedTasks(ctx context.Context, db *sql.DB, project string, byPlan bool, plan string) ([]BlockedTask, error) {
	planClause := ` AND t.plan_slug = ''`
	args := []any{project}
	if byPlan {
		planClause = ` AND t.plan_slug = ?`
		args = append(args, plan)
	}
	rows, err := db.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks t
		WHERE t.status = 'open' AND t.project_slug = ?`+planClause+`
		  AND EXISTS (
		      SELECT 1 FROM task_deps d
		      JOIN tasks b ON b.id = d.depends_on
		      WHERE d.task_id = t.id AND b.status IN ('open','in_progress'))
		ORDER BY t.created_at ASC, t.id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("store.BlockedTasks: %w", err)
	}
	tasks, err := scanTasksWithDeps(ctx, db, rows)
	if err != nil {
		return nil, err
	}
	out := make([]BlockedTask, 0, len(tasks))
	for _, t := range tasks {
		blockers, err := openBlockers(ctx, db, t.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, BlockedTask{Task: t, Blockers: blockers})
	}
	return out, nil
}

// openBlockers returns the open/in_progress dependencies of a task.
func openBlockers(ctx context.Context, db *sql.DB, taskID string) ([]core.Task, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+prefixCols("b", taskCols)+`
		FROM task_deps d JOIN tasks b ON b.id = d.depends_on
		WHERE d.task_id = ? AND b.status IN ('open','in_progress')
		ORDER BY b.created_at ASC, b.id ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("store.openBlockers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []core.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("store.openBlockers: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TasksBlockedBy returns the tasks that depend on id -- the inverse of a task's
// DependsOn edges, i.e. the tasks this one blocks. Unlike openBlockers it applies
// no status filter (closed dependents are included too), oldest-created first. It
// backs the console task peek's reverse "blocks" section.
func TasksBlockedBy(ctx context.Context, db *sql.DB, id string) ([]core.Task, error) {
	if id == "" {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT `+prefixCols("t", taskCols)+`
		FROM task_deps d JOIN tasks t ON t.id = d.task_id
		WHERE d.depends_on = ?
		ORDER BY t.created_at ASC, t.id ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("store.TasksBlockedBy: %w", err)
	}
	return scanTasksWithDeps(ctx, db, rows)
}

// TaskByID returns a task with its dependency ids populated. found is false when
// absent.
func TaskByID(ctx context.Context, db *sql.DB, id string) (core.Task, error) {
	t, ok, err := taskByIDTx(ctx, db, id)
	if err != nil {
		return core.Task{}, err
	}
	if !ok {
		return core.Task{}, fmt.Errorf("store.TaskByID: %w: %q", ErrTaskNotFound, id)
	}
	deps, err := dependsOnOf(ctx, db, id)
	if err != nil {
		return core.Task{}, err
	}
	t.DependsOn = deps
	return t, nil
}

// taskByIDTx loads a task row (without deps) via any querier.
func taskByIDTx(ctx context.Context, q rowQuerier, id string) (core.Task, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ? LIMIT 1`, id)
	if err != nil {
		return core.Task{}, false, fmt.Errorf("store: taskByID: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return core.Task{}, false, rows.Err()
	}
	t, err := scanTask(rows)
	if err != nil {
		return core.Task{}, false, fmt.Errorf("store: scan task: %w", err)
	}
	return t, true, nil
}

// scanTasksWithDeps drains task rows and populates each task's DependsOn ids.
func scanTasksWithDeps(ctx context.Context, db *sql.DB, rows *sql.Rows) ([]core.Task, error) {
	defer func() { _ = rows.Close() }()
	var tasks []core.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan task: %w", err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range tasks {
		deps, err := dependsOnOf(ctx, db, tasks[i].ID)
		if err != nil {
			return nil, err
		}
		tasks[i].DependsOn = deps
	}
	return tasks, nil
}

func scanTask(rows *sql.Rows) (core.Task, error) {
	var (
		t                core.Task
		status           string
		created, updated string
		lease, closed    sql.NullString
	)
	if err := rows.Scan(&t.ID, &t.ProjectSlug, &t.Title, &t.Body, &status,
		&t.CreatedBy, &t.PlanSlug, &t.ClaimedBy, &lease, &created, &updated, &closed); err != nil {
		return core.Task{}, err
	}
	t.Status = core.TaskStatus(status)
	var err error
	if t.CreatedAt, err = core.ParseTime(created); err != nil {
		return core.Task{}, fmt.Errorf("created_at: %w", err)
	}
	if t.UpdatedAt, err = core.ParseTime(updated); err != nil {
		return core.Task{}, fmt.Errorf("updated_at: %w", err)
	}
	if t.LeaseExpiresAt, err = nullTimePtr(lease); err != nil {
		return core.Task{}, fmt.Errorf("lease_expires_at: %w", err)
	}
	if t.ClosedAt, err = nullTimePtr(closed); err != nil {
		return core.Task{}, fmt.Errorf("closed_at: %w", err)
	}
	return t, nil
}

// prefixCols rewrites a comma-separated column list to alias.col form, so a
// joined query can select one table's columns unambiguously.
func prefixCols(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// dedupeStrings returns s with blanks dropped and duplicates removed, order
// preserved.
func dedupeStrings(s []string) []string {
	seen := make(map[string]bool, len(s))
	var out []string
	for _, v := range s {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
