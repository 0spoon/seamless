package mcp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// defaultClaimLease is how long a task claim holds before it lapses and the task
// becomes reclaimable, unless the caller overrides it via lease_seconds.
const defaultClaimLease = 15 * time.Minute

func tasksAddTool() mcp.Tool {
	return mcp.NewTool("tasks_add",
		mcp.WithDescription("Add a task to the dependency-aware ready queue. depends_on lists task ids that must finish first (done or dropped unblocks); each must exist and must not create a cycle. The task is 'ready' once it has no open/in_progress blocker."),
		mcp.WithString("title", mcp.Required(), mcp.Description("short task title")),
		mcp.WithString("body", mcp.Description("optional details / acceptance criteria (aliases: content, text)")),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound/ambient session's project. Pass project=global for a global task. With no session and no explicit project the add is rejected as ambiguous.")),
		mcp.WithArray("depends_on", mcp.WithStringItems(), mcp.Description("task ids this task is blocked by (a comma-separated string is also accepted)")),
		mcp.WithString("plan", mcp.Description("optional plan slug (plan:<slug> convention) that composes this task as a step of a plan. Plan steps are excluded from the default ready-queue and surfaced under the plan filter.")),
	)
}

func (s *Server) handleTasksAdd(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	title := argString(req, "title")
	if title == "" {
		return errResult("tasks_add", errors.New("title is required"))
	}
	project, err := s.resolveWriteScope(ctx, argString(req, "project"))
	if err != nil {
		return errResult("tasks_add", err)
	}
	id, err := core.NewID()
	if err != nil {
		return errResult("tasks_add", err)
	}
	now := time.Now().UTC()
	task := core.Task{
		ID: id, ProjectSlug: project, Title: title, Body: argRaw(req, "body"),
		Status: core.TaskOpen, CreatedBy: s.boundSessionName(ctx),
		PlanSlug:  argString(req, "plan"),
		DependsOn: argStrings(req, "depends_on"),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateTask(ctx, s.cfg.DB, task); err != nil {
		return errResult("tasks_add", err)
	}
	created, err := store.TaskByID(ctx, s.cfg.DB, id)
	if err != nil {
		return errResult("tasks_add", err)
	}
	s.record(ctx, core.EventTaskTransition, s.boundSession(ctx), project, id,
		map[string]any{"to": string(core.TaskOpen), "created": true})
	return jsonResult(taskJSON(created))
}

func tasksUpdateTool() mcp.Tool {
	return mcp.NewTool("tasks_update",
		mcp.WithDescription("Update a task: change status (open|in_progress|done|dropped), edit title/body, or add dependencies. Moving to done/dropped closes it and unblocks its dependents. A task another session holds via a live claim is locked to its holder: updating it fails with 'already claimed' until the lease lapses or the holder releases it."),
		mcp.WithString("id", mcp.Required(), mcp.Description("task id")),
		mcp.WithString("status", mcp.Enum("open", "in_progress", "done", "dropped"), mcp.Description("new status")),
		mcp.WithString("title", mcp.Description("new title")),
		mcp.WithString("body", mcp.Description("new body (aliases: content, text)")),
		mcp.WithString("project", mcp.Description("reassign the task to another project slug (used when a split moves a project's open work to a child)")),
		mcp.WithArray("add_depends_on", mcp.WithStringItems(), mcp.Description("task ids to add as blockers (a comma-separated string is also accepted)")),
	)
}

func (s *Server) handleTasksUpdate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := argString(req, "id")
	if id == "" {
		return errResult("tasks_update", errors.New("id is required"))
	}
	var patch store.TaskPatch
	if st := argString(req, "status"); st != "" {
		status := core.TaskStatus(st)
		if !status.Valid() {
			return errResult("tasks_update", fmt.Errorf("invalid status %q", st))
		}
		patch.Status = &status
	}
	if argPresent(req, "title") {
		title := argString(req, "title")
		if title == "" {
			return errResult("tasks_update", errors.New("title must not be blank; omit it to keep the current title"))
		}
		patch.Title = &title
	}
	if argPresent(req, "body") {
		body := argRaw(req, "body")
		patch.Body = &body
	}
	if argPresent(req, "project") {
		project, perr := validateProjectArg(argString(req, "project"))
		if perr != nil {
			return errResult("tasks_update", perr)
		}
		patch.ProjectSlug = &project
	}
	patch.AddDependsOn = argStrings(req, "add_depends_on")

	if patch.Status == nil && patch.Title == nil && patch.Body == nil && patch.ProjectSlug == nil && len(patch.AddDependsOn) == 0 {
		return errResult("tasks_update", errors.New("nothing to update: pass status, title, body, project, or add_depends_on"))
	}
	updated, err := store.UpdateTask(ctx, s.cfg.DB, id, patch, s.boundSession(ctx), time.Now().UTC())
	if err != nil {
		return errResult("tasks_update", err)
	}
	s.record(ctx, core.EventTaskTransition, s.boundSession(ctx), updated.ProjectSlug, id,
		map[string]any{"to": string(updated.Status)})
	return jsonResult(taskJSON(updated))
}

func tasksReadyTool() mcp.Tool {
	return mcp.NewTool("tasks_ready",
		mcp.WithDescription("List the actionable (ready) tasks for a project -- open tasks with no unfinished blocker -- oldest first, plus the blocked tasks with their still-open blockers. By default plan-step tasks are excluded; pass plan=<slug> to list that plan's steps instead."),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound session's project")),
		mcp.WithString("plan", mcp.Description("optional plan slug: return that plan's ready/blocked step tasks instead of the default (non-plan) queue")),
	)
}

func (s *Server) handleTasksReady(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	project, err := s.resolveReadScope(ctx, argString(req, "project"))
	if err != nil {
		return errResult("tasks_ready", err)
	}
	plan := argString(req, "plan")
	var ready []core.Task
	var blocked []store.BlockedTask
	if plan != "" {
		ready, err = store.ReadyTasksForPlan(ctx, s.cfg.DB, project, plan)
		if err != nil {
			return errResult("tasks_ready", err)
		}
		blocked, err = store.BlockedTasksForPlan(ctx, s.cfg.DB, project, plan)
	} else {
		ready, err = store.ReadyTasks(ctx, s.cfg.DB, project)
		if err != nil {
			return errResult("tasks_ready", err)
		}
		blocked, err = store.BlockedTasks(ctx, s.cfg.DB, project)
	}
	if err != nil {
		return errResult("tasks_ready", err)
	}
	readyJSON := make([]map[string]any, 0, len(ready))
	for _, t := range ready {
		readyJSON = append(readyJSON, taskJSON(t))
	}
	blockedJSON := make([]map[string]any, 0, len(blocked))
	for _, b := range blocked {
		j := taskJSON(b.Task)
		blockers := make([]map[string]any, 0, len(b.Blockers))
		for _, bl := range b.Blockers {
			blockers = append(blockers, map[string]any{"id": bl.ID, "title": bl.Title, "status": string(bl.Status)})
		}
		j["blockers"] = blockers
		blockedJSON = append(blockedJSON, j)
	}
	return jsonResult(map[string]any{"project": project, "ready": readyJSON, "blocked": blockedJSON})
}

func tasksListTool() mcp.Tool {
	return mcp.NewTool("tasks_list",
		mcp.WithDescription("List a project's tasks, optionally filtered by status, newest first. By default plan-step tasks are excluded; pass plan=<slug> to list that plan's steps instead. Pass id=<task id> to load a single task by its globally-unique id (a direct lookup that ignores project/status/plan and needs no session scope)."),
		mcp.WithString("id", mcp.Description("load exactly one task by its globally-unique id; when set, project/status/plan are ignored and the response's tasks array holds just that task")),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound session's project")),
		mcp.WithString("status", mcp.Enum("open", "in_progress", "done", "dropped"), mcp.Description("optional status filter")),
		mcp.WithString("plan", mcp.Description("optional plan slug: list that plan's step tasks instead of the default (non-plan) tasks")),
	)
}

func (s *Server) handleTasksList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// id is a globally-unique ULID, so a by-id load is a direct lookup that needs
	// no project scope -- an agent can load a task it only knows the id of.
	if id := argString(req, "id"); id != "" {
		task, err := store.TaskByID(ctx, s.cfg.DB, id)
		if err != nil {
			return errResult("tasks_list", err)
		}
		return jsonResult(map[string]any{
			"project": task.ProjectSlug,
			"tasks":   []map[string]any{taskJSON(task)},
		})
	}
	project, err := s.resolveReadScope(ctx, argString(req, "project"))
	if err != nil {
		return errResult("tasks_list", err)
	}
	var status core.TaskStatus
	if st := argString(req, "status"); st != "" {
		status = core.TaskStatus(st)
		if !status.Valid() {
			return errResult("tasks_list", fmt.Errorf("invalid status %q", st))
		}
	}
	var tasks []core.Task
	if plan := argString(req, "plan"); plan != "" {
		tasks, err = store.ListTasksForPlan(ctx, s.cfg.DB, project, status, plan)
	} else {
		tasks, err = store.ListTasks(ctx, s.cfg.DB, project, status)
	}
	if err != nil {
		return errResult("tasks_list", err)
	}
	out := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskJSON(t))
	}
	return jsonResult(map[string]any{"project": project, "tasks": out})
}

func tasksClaimTool() mcp.Tool {
	return mcp.NewTool("tasks_claim",
		mcp.WithDescription("Atomically claim a task for the current session, moving it to in_progress with a lease. Fails if another live claim already holds it. Re-claiming a task you already hold refreshes (heartbeats) the lease; a task whose lease has expired can be reclaimed. Release it with tasks_release or by closing it (tasks_update done/dropped); session_end releases all of a session's claims."),
		mcp.WithString("id", mcp.Required(), mcp.Description("task id to claim")),
		// No Min/Max: the handler owns this parameter's range, because its upper
		// bound is an int64-overflow hazard in the seconds->Duration conversion
		// rather than a domain limit a schema could state. The validator owns the
		// type; the handler owns range and domain.
		mcp.WithNumber("lease_seconds", mcp.Description("lease duration in seconds before the claim lapses and the task becomes reclaimable (default 900)")),
	)
}

func (s *Server) handleTasksClaim(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := argString(req, "id")
	if id == "" {
		return errResult("tasks_claim", errors.New("id is required"))
	}
	sessionID := s.boundSession(ctx)
	if sessionID == "" {
		return errResult("tasks_claim", errors.New("no active session to claim as; start a session first"))
	}
	lease := defaultClaimLease
	if argPresent(req, "lease_seconds") {
		secs := argInt(req, "lease_seconds", 0)
		// The upper bound guards int64 overflow in the seconds->Duration
		// conversion: an overflowed lease goes negative, so the claim would
		// report success while being instantly expired (silently reclaimable).
		if secs <= 0 || int64(secs) > int64(math.MaxInt64/time.Second) {
			return errResult("tasks_claim", fmt.Errorf("invalid lease_seconds %d", secs))
		}
		lease = time.Duration(secs) * time.Second
	}
	res, err := store.ClaimTask(ctx, s.cfg.DB, id, sessionID, lease, time.Now().UTC())
	if err != nil {
		return errResult("tasks_claim", err)
	}
	payload := map[string]any{"to": string(res.Task.Status), "claimed_by": sessionID}
	if res.Reclaimed {
		payload["reclaimed"] = true
		payload["prior_holder"] = res.PriorHolder
	}
	s.record(ctx, core.EventTaskTransition, sessionID, res.Task.ProjectSlug, id, payload)
	return jsonResult(taskJSON(res.Task))
}

func tasksReleaseTool() mcp.Tool {
	return mcp.NewTool("tasks_release",
		mcp.WithDescription("Release a task the current session holds, reopening it (status back to open, claim cleared) so another agent can claim it. Only the current holder may release."),
		mcp.WithString("id", mcp.Required(), mcp.Description("task id to release")),
	)
}

func (s *Server) handleTasksRelease(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := argString(req, "id")
	if id == "" {
		return errResult("tasks_release", errors.New("id is required"))
	}
	sessionID := s.boundSession(ctx)
	if sessionID == "" {
		return errResult("tasks_release", errors.New("no active session; nothing to release"))
	}
	released, err := store.ReleaseTask(ctx, s.cfg.DB, id, sessionID, time.Now().UTC())
	if err != nil {
		return errResult("tasks_release", err)
	}
	s.record(ctx, core.EventTaskTransition, sessionID, released.ProjectSlug, id,
		map[string]any{"to": string(released.Status), "released": true})
	return jsonResult(taskJSON(released))
}

// taskJSON renders a task for a tool response.
func taskJSON(t core.Task) map[string]any {
	j := map[string]any{
		"id": t.ID, "title": t.Title, "status": string(t.Status),
		"project": t.ProjectSlug, "created_by": t.CreatedBy,
	}
	if t.Body != "" {
		j["body"] = t.Body
	}
	if t.PlanSlug != "" {
		j["plan"] = t.PlanSlug
	}
	if t.ClaimedBy != "" {
		j["claimed_by"] = t.ClaimedBy
	}
	if t.LeaseExpiresAt != nil {
		j["lease_expires_at"] = core.FormatTime(*t.LeaseExpiresAt)
	}
	if len(t.DependsOn) > 0 {
		j["depends_on"] = t.DependsOn
	}
	return j
}

// boundSessionName resolves the bound (or ambient-fallback) session's name for a
// task's created_by field, or "" when there is no session.
func (s *Server) boundSessionName(ctx context.Context) string {
	id := s.boundSession(ctx)
	if id == "" {
		return ""
	}
	if sess, ok, err := store.SessionByID(ctx, s.cfg.DB, id); err == nil && ok {
		return sess.Name
	}
	return ""
}
