// The task read surface: the ready queue, the blocked list with each task's
// blockers, and the plain per-project/per-plan listings. "Ready" means open with
// no blocker still open or in_progress -- one rule, repeated as a NOT EXISTS in
// each query here and mirrored by ClaimTask's branch (a) and ActivePlans.
package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/0spoon/seamless/internal/core"
)

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
