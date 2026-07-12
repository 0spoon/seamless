package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

func tasksAddTool() mcp.Tool {
	return mcp.NewTool("tasks_add",
		mcp.WithDescription("Add a task to the dependency-aware ready queue. depends_on lists task ids that must finish first (done or dropped unblocks); each must exist and must not create a cycle. The task is 'ready' once it has no open/in_progress blocker."),
		mcp.WithString("title", mcp.Required(), mcp.Description("short task title")),
		mcp.WithString("body", mcp.Description("optional details / acceptance criteria (aliases: content, text)")),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound/ambient session's project. Pass project=global for a global task. With no session and no explicit project the add is rejected as ambiguous.")),
		mcp.WithString("depends_on", mcp.Description("comma-separated task ids this task is blocked by")),
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
		ID: id, ProjectSlug: project, Title: title, Body: argBody(req),
		Status: core.TaskOpen, CreatedBy: s.boundSessionName(ctx),
		DependsOn: parseCommaList(argString(req, "depends_on")),
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
		mcp.WithDescription("Update a task: change status (open|in_progress|done|dropped), edit title/body, or add dependencies. Moving to done/dropped closes it and unblocks its dependents."),
		mcp.WithString("id", mcp.Required(), mcp.Description("task id")),
		mcp.WithString("status", mcp.Enum("open", "in_progress", "done", "dropped"), mcp.Description("new status")),
		mcp.WithString("title", mcp.Description("new title")),
		mcp.WithString("body", mcp.Description("new body (aliases: content, text)")),
		mcp.WithString("add_depends_on", mcp.Description("comma-separated task ids to add as blockers")),
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
	if req.GetArguments()["title"] != nil {
		title := argString(req, "title")
		patch.Title = &title
	}
	if body, ok := firstStringArg(req.GetArguments(), "body", "content", "text"); ok {
		patch.Body = &body
	}
	patch.AddDependsOn = parseCommaList(argString(req, "add_depends_on"))

	if patch.Status == nil && patch.Title == nil && patch.Body == nil && len(patch.AddDependsOn) == 0 {
		return errResult("tasks_update", errors.New("nothing to update: pass status, title, body, or add_depends_on"))
	}
	updated, err := store.UpdateTask(ctx, s.cfg.DB, id, patch, time.Now().UTC())
	if err != nil {
		return errResult("tasks_update", err)
	}
	s.record(ctx, core.EventTaskTransition, s.boundSession(ctx), updated.ProjectSlug, id,
		map[string]any{"to": string(updated.Status)})
	return jsonResult(taskJSON(updated))
}

func tasksReadyTool() mcp.Tool {
	return mcp.NewTool("tasks_ready",
		mcp.WithDescription("List the actionable (ready) tasks for a project -- open tasks with no unfinished blocker -- oldest first, plus the blocked tasks with their still-open blockers."),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound session's project")),
	)
}

func (s *Server) handleTasksReady(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	project := s.resolveProject(ctx, argString(req, "project"))
	ready, err := store.ReadyTasks(ctx, s.cfg.DB, project)
	if err != nil {
		return errResult("tasks_ready", err)
	}
	blocked, err := store.BlockedTasks(ctx, s.cfg.DB, project)
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
		mcp.WithDescription("List a project's tasks, optionally filtered by status, newest first."),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound session's project")),
		mcp.WithString("status", mcp.Enum("open", "in_progress", "done", "dropped"), mcp.Description("optional status filter")),
	)
}

func (s *Server) handleTasksList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	project := s.resolveProject(ctx, argString(req, "project"))
	var status core.TaskStatus
	if st := argString(req, "status"); st != "" {
		status = core.TaskStatus(st)
		if !status.Valid() {
			return errResult("tasks_list", fmt.Errorf("invalid status %q", st))
		}
	}
	tasks, err := store.ListTasks(ctx, s.cfg.DB, project, status)
	if err != nil {
		return errResult("tasks_list", err)
	}
	out := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskJSON(t))
	}
	return jsonResult(map[string]any{"project": project, "tasks": out})
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
