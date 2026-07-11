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
	created_at, updated_at, closed_at`

// cycleWalkCap bounds the dependency walk in createsCycle, a backstop against a
// pathological graph (the edges form a DAG by construction, so this is never hit
// in practice).
const cycleWalkCap = 10000

// ErrTaskCycle is returned when adding a dependency edge would create a cycle.
var ErrTaskCycle = errors.New("dependency would create a cycle")

// ErrTaskNotFound is returned when a task id does not exist (e.g. a dangling
// depends_on reference).
var ErrTaskNotFound = errors.New("task not found")

// rowQuerier is the read subset shared by *sql.DB and *sql.Tx, so the cycle and
// dangling-reference checks run identically inside or outside a transaction.
type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// TaskPatch is the set of mutable fields UpdateTask may change; nil fields are
// left untouched. AddDependsOn edges are added (existing edges are kept).
type TaskPatch struct {
	Status       *core.TaskStatus
	Title        *string
	Body         *string
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
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tasks (id, project_slug, title, body, status, created_by,
		                   created_at, updated_at, closed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ProjectSlug, t.Title, t.Body, string(t.Status), t.CreatedBy,
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
func UpdateTask(ctx context.Context, db *sql.DB, id string, patch TaskPatch, now time.Time) (core.Task, error) {
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

	if patch.Title != nil {
		t.Title = *patch.Title
	}
	if patch.Body != nil {
		t.Body = *patch.Body
	}
	if patch.Status != nil && *patch.Status != t.Status {
		t.Status = *patch.Status
		if t.Status.Closed() {
			at := now.UTC()
			t.ClosedAt = &at
		} else {
			t.ClosedAt = nil
		}
	}
	t.UpdatedAt = now.UTC()

	var closedAt any
	if t.ClosedAt != nil {
		closedAt = core.FormatTime(*t.ClosedAt)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE tasks SET title = ?, body = ?, status = ?, updated_at = ?, closed_at = ?
		 WHERE id = ?`,
		t.Title, t.Body, string(t.Status), core.FormatTime(t.UpdatedAt), closedAt, t.ID); err != nil {
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
		WHERE t.status = 'open' AND t.project_slug = ?
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

// ListTasks returns a project's tasks, optionally filtered by status, newest
// first. An empty status returns every status.
func ListTasks(ctx context.Context, db *sql.DB, project string, status core.TaskStatus) ([]core.Task, error) {
	query := `SELECT ` + taskCols + ` FROM tasks WHERE project_slug = ?`
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

// BlockedTask is an open task that is not ready, paired with the blockers still
// keeping it off the ready queue (open/in_progress dependencies).
type BlockedTask struct {
	Task     core.Task   `json:"task"`
	Blockers []core.Task `json:"blockers"`
}

// BlockedTasks returns a project's open-but-not-ready tasks, each with its
// still-open blockers, so a caller can render the dependency chain legibly.
func BlockedTasks(ctx context.Context, db *sql.DB, project string) ([]BlockedTask, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks t
		WHERE t.status = 'open' AND t.project_slug = ?
		  AND EXISTS (
		      SELECT 1 FROM task_deps d
		      JOIN tasks b ON b.id = d.depends_on
		      WHERE d.task_id = t.id AND b.status IN ('open','in_progress'))
		ORDER BY t.created_at ASC, t.id ASC`, project)
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
		closed           sql.NullString
	)
	if err := rows.Scan(&t.ID, &t.ProjectSlug, &t.Title, &t.Body, &status,
		&t.CreatedBy, &created, &updated, &closed); err != nil {
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
