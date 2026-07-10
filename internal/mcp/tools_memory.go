package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

const maxDescriptionRunes = 150

func memoryWriteTool() mcp.Tool {
	return mcp.NewTool("memory_write",
		mcp.WithDescription("Create or update a durable memory. Writing an existing name updates it in place (its id is stable). On a new name, a semantically similar existing memory is reported as an advisory hint; the write still proceeds."),
		mcp.WithString("name", mcp.Required(), mcp.Description("kebab-case identifier, unique within the project")),
		mcp.WithString("kind", mcp.Required(), mcp.Enum("constraint", "runbook", "protocol", "gotcha", "decision", "refuted", "reference", "stage"), mcp.Description("memory kind")),
		mcp.WithString("description", mcp.Required(), mcp.Description("one line, <=150 chars -- the only text shown in indexes")),
		mcp.WithString("body", mcp.Required(), mcp.Description("markdown body")),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound session's project")),
	)
}

func (s *Server) handleMemoryWrite(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req, "name")
	kindStr := argString(req, "kind")
	desc := argString(req, "description")
	body := argRaw(req, "body")
	project := s.resolveProject(ctx, argString(req, "project"))

	if name == "" || kindStr == "" || desc == "" || strings.TrimSpace(body) == "" {
		return errResult("memory_write", errors.New("name, kind, description, and body are required"))
	}
	kind := core.MemoryKind(kindStr)
	if !kind.Valid() {
		return errResult("memory_write", fmt.Errorf("invalid kind %q", kindStr))
	}
	if len([]rune(desc)) > maxDescriptionRunes {
		desc = string([]rune(desc)[:maxDescriptionRunes])
	}

	now := time.Now().UTC()
	existing, found, err := s.resolveMemory(ctx, project, name, false)
	if err != nil {
		return errResult("memory_write", err)
	}

	mem := core.Memory{
		Kind: kind, Name: name, Description: desc, Project: project, Body: body,
		Updated: now, ValidFrom: now, SourceSession: s.boundSession(ctx),
	}
	var similar *map[string]any
	if found {
		// Update in place: the ULID and creation provenance are identity and
		// must not change just because the content did.
		mem.ID = existing.ID
		mem.Created = existing.Created
		if !existing.ValidFrom.IsZero() {
			mem.ValidFrom = existing.ValidFrom
		}
		if existing.SourceSession != "" {
			mem.SourceSession = existing.SourceSession
		}
	} else {
		id, err := core.NewID()
		if err != nil {
			return errResult("memory_write", err)
		}
		mem.ID = id
		mem.Created = now
		if hint, herr := s.cfg.Retrieve.DedupHint(ctx, project, name, desc); herr == nil && hint != nil {
			similar = &map[string]any{"name": hint.Name, "description": hint.Description, "score": hint.Score}
		}
	}

	written, err := s.cfg.Files.WriteMemory(ctx, mem)
	if err != nil {
		return errResult("memory_write", err)
	}
	s.record(ctx, core.EventMemoryWritten, s.boundSession(ctx), project, written.ID,
		map[string]any{"name": name, "kind": kindStr, "updated": found})

	resp := map[string]any{"id": written.ID, "name": name, "project": project, "updated": found}
	if similar != nil {
		resp["similar"] = *similar
	}
	return jsonResult(resp)
}

func memoryAppendTool() mcp.Tool {
	return mcp.NewTool("memory_append",
		mcp.WithDescription("Append content to an existing memory's body. The memory keeps its id."),
		mcp.WithString("name", mcp.Required(), mcp.Description("memory name")),
		mcp.WithString("content", mcp.Required(), mcp.Description("markdown to append")),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound session's project")),
	)
}

func (s *Server) handleMemoryAppend(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req, "name")
	content := argRaw(req, "content")
	if name == "" || strings.TrimSpace(content) == "" {
		return errResult("memory_append", errors.New("name and content are required"))
	}
	project := s.resolveProject(ctx, argString(req, "project"))
	idx, found, err := s.resolveMemory(ctx, project, name, false)
	if err != nil {
		return errResult("memory_append", err)
	}
	if !found {
		return errResult("memory_append", fmt.Errorf("no memory named %q", name))
	}
	// Read the full memory (index rows have no body) and append.
	mem, err := s.cfg.Files.Store().ReadMemory(idx.FilePath)
	if err != nil {
		return errResult("memory_append", err)
	}
	mem.Body = strings.TrimRight(mem.Body, "\n") + "\n" + content + "\n"
	mem.Updated = time.Now().UTC()
	if _, err := s.cfg.Files.WriteMemory(ctx, mem); err != nil {
		return errResult("memory_append", err)
	}
	s.record(ctx, core.EventMemoryWritten, s.boundSession(ctx), mem.Project, mem.ID,
		map[string]any{"name": name, "appended": true})
	return jsonResult(map[string]any{"id": mem.ID, "name": name, "status": "appended"})
}

func memoryReadTool() mcp.Tool {
	return mcp.NewTool("memory_read",
		mcp.WithDescription("Read a memory by name within the current project, falling back to a global memory of the same name."),
		mcp.WithString("name", mcp.Required(), mcp.Description("memory name")),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound session's project")),
	)
}

func (s *Server) handleMemoryRead(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req, "name")
	if name == "" {
		return errResult("memory_read", errors.New("name is required"))
	}
	project := s.resolveProject(ctx, argString(req, "project"))
	idx, found, err := s.resolveMemory(ctx, project, name, true)
	if err != nil {
		return errResult("memory_read", err)
	}
	if !found {
		return errResult("memory_read", fmt.Errorf("no memory named %q", name))
	}
	mem, err := s.cfg.Files.Store().ReadMemory(idx.FilePath)
	if err != nil {
		return errResult("memory_read", err)
	}
	s.record(ctx, core.EventMemoryRead, s.boundSession(ctx), mem.Project, mem.ID, map[string]any{"name": name})
	return jsonResult(map[string]any{
		"id": mem.ID, "kind": string(mem.Kind), "name": mem.Name,
		"description": mem.Description, "project": mem.Project, "body": mem.Body,
		"tags": mem.Tags,
	})
}

func memoryDeleteTool() mcp.Tool {
	return mcp.NewTool("memory_delete",
		mcp.WithDescription("Delete a memory by name (removes the file and its index)."),
		mcp.WithString("name", mcp.Required(), mcp.Description("memory name")),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound session's project")),
	)
}

func (s *Server) handleMemoryDelete(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req, "name")
	if name == "" {
		return errResult("memory_delete", errors.New("name is required"))
	}
	project := s.resolveProject(ctx, argString(req, "project"))
	idx, found, err := s.resolveMemory(ctx, project, name, true)
	if err != nil {
		return errResult("memory_delete", err)
	}
	if !found {
		return errResult("memory_delete", fmt.Errorf("no memory named %q", name))
	}
	if err := s.cfg.Files.Remove(ctx, idx.FilePath); err != nil {
		return errResult("memory_delete", err)
	}
	s.record(ctx, core.EventMemoryArchived, s.boundSession(ctx), idx.Project, idx.ID, map[string]any{"name": name})
	return jsonResult(map[string]any{"status": "deleted", "id": idx.ID, "name": name})
}

// resolveMemory finds an active memory by (project, name); when globalFallback
// is set and none is found in the project scope, it retries the global scope.
func (s *Server) resolveMemory(ctx context.Context, project, name string, globalFallback bool) (core.Memory, bool, error) {
	m, ok, err := store.MemoryByName(ctx, s.cfg.DB, project, name)
	if err != nil || ok {
		return m, ok, err
	}
	if globalFallback && project != "" {
		return store.MemoryByName(ctx, s.cfg.DB, "", name)
	}
	return core.Memory{}, false, nil
}
