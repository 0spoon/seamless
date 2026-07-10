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

func projectListTool() mcp.Tool {
	return mcp.NewTool("project_list",
		mcp.WithDescription("List all projects (slug, name, description)."),
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
	return mcp.NewTool("project_create",
		mcp.WithDescription("Create a project. The slug defaults to a slugified name."),
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
		slug = slugify(name)
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
