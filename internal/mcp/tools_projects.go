package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
	"github.com/0spoon/seamless/internal/validate"
)

func projectListTool() mcp.Tool {
	return mcp.NewTool("project_list", hintRead(),
		mcp.WithDescription("List every project (slug, name, description). Use it to learn the exact slug before a deliberate cross-project write or a project_create -- coining a near-duplicate of a slug that already exists is the failure this prevents -- and to see whether work already has a home. You usually do NOT need it to pick a scope: memory, note, and task calls inherit the project from the session binding, and passing project= is for writing outside that on purpose. It returns identity, not contents; to search what is inside a project, use recall."),
	)
}

func (s *Server) handleProjectList(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ps, err := store.ListProjects(ctx, s.cfg.DB)
	if err != nil {
		return errResult("project_list", err)
	}
	out := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, map[string]any{
			"id": p.ID, "slug": p.Slug, "name": p.Name, "description": p.Description,
		})
	}
	return jsonResult(map[string]any{"projects": out})
}

func projectCreateTool() mcp.Tool {
	return mcp.NewTool("project_create", hintSet(),
		mcp.WithDescription("Register a project up front, with a human-readable name and an optional description. You rarely need this: any durable write naming an unknown project slug (memory_write, notes_create, tasks_add, capture_url, trial_record) already registers that project, and a git repo maps itself to one on its first session. Reach for this only to give a project a proper name and description BEFORE anything is written into it, or to create one you will not write to yet -- an auto-registered project is named after its own slug until someone fixes it. Call project_list first: coining a near-duplicate of an existing slug is the failure mode here. To divide an existing project into children, use gardener_split rather than creating them by hand. The slug defaults to a slugified name; \"global\" and \"all\" are reserved, and an existing slug is an error, not an update -- this never renames or edits a project."),
		mcp.WithString("name", mcp.Required(), mcp.Description("human-readable project name")),
		mcp.WithString("slug", mcp.Description("optional explicit slug")),
		mcp.WithString("description", mcp.Description("optional one-line description")),
	)
}

func (s *Server) handleProjectCreate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req, "name")
	if name == "" {
		return errResult("project_create", errors.New("name is required"))
	}
	slug := argString(req, "slug")
	if slug == "" {
		slug = core.Slugify(name)
	}
	if normalizeProject(slug) == "" {
		return errResult("project_create", fmt.Errorf("slug %q is reserved for the global namespace", slug))
	}
	// Without this, a project could take the name of the widening token and then
	// be permanently unreachable through it -- gardener_request reads "all" before
	// it resolves anything as a slug.
	if slug == allProjectsToken {
		return errResult("project_create", fmt.Errorf("slug %q is reserved: gardener_request reads it as every project", slug))
	}
	// The slug becomes a directory under the memory/ and notes/ trees; reject
	// separators and ".." so no later write can escape its tree.
	if err := validate.Name(slug); err != nil {
		return errResult("project_create", fmt.Errorf("invalid slug %q: %w", slug, err))
	}
	id, err := core.NewID()
	if err != nil {
		return errResult("project_create", err)
	}
	now := time.Now().UTC()
	p := core.Project{ID: id, Slug: slug, Name: name, Description: argString(req, "description"), CreatedAt: now, UpdatedAt: now}
	if err := store.CreateProject(ctx, s.cfg.DB, p); err != nil {
		if errors.Is(err, store.ErrSlugExists) {
			return errResult("project_create", fmt.Errorf("project slug %q already exists", slug))
		}
		return errResult("project_create", err)
	}
	return jsonResult(map[string]any{"project_id": id, "slug": slug, "name": name})
}
