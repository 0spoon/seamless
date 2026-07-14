package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/lifecycle"
	"github.com/0spoon/seamless/internal/store"
)

const maxDescriptionRunes = 150

func memoryWriteTool() mcp.Tool {
	return mcp.NewTool("memory_write",
		mcp.WithDescription("Create or update a durable memory. Writing an existing name updates it in place (its id is stable). On a new name, a semantically similar existing memory is reported as an advisory hint; the write still proceeds. Pass supersedes to replace a DIFFERENT, now-outdated memory: it is marked invalid and leaves every index (briefing, recall) but stays readable with a pointer here. If the supersede step fails the new memory is still written and kept, but the call returns an error naming it -- the target is then still active."),
		mcp.WithString("name", mcp.Required(), mcp.Description("kebab-case identifier, unique within the project")),
		mcp.WithString("kind", mcp.Required(), mcp.Enum("constraint", "runbook", "protocol", "gotcha", "decision", "refuted", "reference", "stage"), mcp.Description("memory kind")),
		mcp.WithString("description", mcp.Required(), mcp.Description("one line, <=150 chars -- the only text shown in indexes")),
		mcp.WithString("body", mcp.Required(), mcp.Description("markdown body (aliases: content, text)")),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound/ambient session's project. Pass project=global to deliberately create a global (cross-project) memory. With no session and no explicit project the write is rejected as ambiguous.")),
		mcp.WithString("supersedes", mcp.Description("name of an existing memory this one replaces; that memory is marked superseded (invalid) and pointed here")),
	)
}

func (s *Server) handleMemoryWrite(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req, "name")
	kindStr := argString(req, "kind")
	desc := argString(req, "description")
	body := argBody(req)

	if name == "" || kindStr == "" || desc == "" || strings.TrimSpace(body) == "" {
		return errResult("memory_write", errors.New("name, kind, description, and body are required"))
	}
	kind := core.MemoryKind(kindStr)
	if !kind.Valid() {
		return errResult("memory_write", fmt.Errorf("invalid kind %q", kindStr))
	}
	project, err := s.resolveWriteScope(ctx, argString(req, "project"))
	if err != nil {
		return errResult("memory_write", err)
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
		if errors.Is(err, files.ErrPathOccupied) {
			return errResult("memory_write", fmt.Errorf(
				"name %q is still held by a superseded or archived memory (%v) -- free it with memory_delete or pick a different name", name, err))
		}
		return errResult("memory_write", err)
	}
	s.record(ctx, core.EventMemoryWritten, s.boundSession(ctx), project, written.ID,
		map[string]any{"name": name, "kind": kindStr, "updated": found})

	resp := map[string]any{"id": written.ID, "name": name, "project": project, "updated": found}
	if similar != nil {
		resp["similar"] = *similar
	}
	if supersedes := argString(req, "supersedes"); supersedes != "" {
		superseded, serr := s.supersedeMemory(ctx, project, supersedes, written, now)
		if serr != nil {
			// Partial failure: the new memory is kept -- its content is valid
			// knowledge, an update-in-place has no previous body to restore, and
			// re-writing the same name is a lossless in-place update, so fixing
			// the target and retrying is safe. But the supersession did NOT
			// happen, so this must be an explicit tool error: an error field
			// embedded in a success payload reads as success, and the agent
			// would leave the old memory live alongside its replacement.
			return errResult("memory_write", fmt.Errorf(
				"memory %q written and kept (id %s), but superseding %q failed: %w -- %q is STILL ACTIVE; fix the supersedes target and retry (re-writing %q updates it in place)",
				name, written.ID, supersedes, serr, supersedes, name))
		}
		if superseded != "" {
			resp["superseded"] = superseded
		}
	}
	return jsonResult(resp)
}

// supersedeMemory marks the memory named target (in project, falling back to
// global) as superseded by replacement. It returns the superseded memory's name
// (project-qualified) on success, "" when the target IS the replacement (an
// in-place update, nothing to supersede), or an error the caller surfaces as a
// tool error (the write itself is kept). A target already superseded by this
// same replacement reports success, so retrying a memory_write whose supersede
// already landed stays idempotent.
func (s *Server) supersedeMemory(ctx context.Context, project, target string, replacement core.Memory, now time.Time) (string, error) {
	old, found, err := s.resolveMemory(ctx, project, target, true)
	if err != nil {
		return "", err
	}
	if !found {
		// The active index missed: either the name is wrong, or the target is
		// already invalid. Already superseded by this exact replacement is the
		// goal state (a retried call), not a failure.
		prev, ok, perr := s.resolveSupersededMemory(ctx, project, target)
		if perr != nil {
			return "", perr
		}
		if ok && prev.SupersededBy == replacement.ID {
			return lifecycle.MemoryRef(prev.Project, prev.Name), nil
		}
		if ok {
			return "", fmt.Errorf("memory %q is already superseded or archived", target)
		}
		return "", fmt.Errorf("no memory named %q to supersede", target)
	}
	if old.ID == replacement.ID {
		return "", nil // same memory: an in-place update, not a supersession
	}
	// Index rows carry no body; read the file so the tombstone appends to the
	// real content rather than truncating it.
	full, err := s.cfg.Files.Store().ReadMemory(old.FilePath)
	if err != nil {
		return "", err
	}
	updated, err := lifecycle.Supersede(ctx, s.cfg.Files, full, replacement, now)
	if err != nil {
		return "", err
	}
	s.record(ctx, core.EventMemorySuperseded, s.boundSession(ctx), updated.Project, updated.ID,
		map[string]any{"name": updated.Name, "superseded_by": replacement.ID})
	return lifecycle.MemoryRef(updated.Project, updated.Name), nil
}

func memoryAppendTool() mcp.Tool {
	return mcp.NewTool("memory_append",
		mcp.WithDescription("Append markdown to an existing memory's body. The memory keeps its id. To create a new memory, use memory_write."),
		mcp.WithString("name", mcp.Required(), mcp.Description("memory name")),
		mcp.WithString("body", mcp.Required(), mcp.Description("markdown to append (aliases: content, text)")),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound/ambient session's project, then global. Pass project=global to target a global memory.")),
	)
}

func (s *Server) handleMemoryAppend(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req, "name")
	content := argBody(req)
	if name == "" || strings.TrimSpace(content) == "" {
		return errResult("memory_append", errors.New("name and a non-empty body are required (body aliases: content, text)"))
	}
	project, err := s.resolveReadScope(ctx, argString(req, "project"))
	if err != nil {
		return errResult("memory_append", err)
	}
	// Look up in the project scope, falling back to global (as memory_read does)
	// so an append does not miss a global memory the session is scoped to.
	idx, found, err := s.resolveMemory(ctx, project, name, true)
	if err != nil {
		return errResult("memory_append", err)
	}
	if !found {
		return errResult("memory_append", fmt.Errorf("%w; create it first with memory_write, or pass project=<slug> / project=global", scopedNotFound("memory", project, name)))
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
		return errResult("memory_read", errors.New("name is required (memory_read reads one memory by exact name; to search text use recall)"))
	}
	project, err := s.resolveReadScope(ctx, argString(req, "project"))
	if err != nil {
		return errResult("memory_read", err)
	}
	idx, found, err := s.resolveMemory(ctx, project, name, true)
	if err != nil {
		return errResult("memory_read", err)
	}
	if !found {
		// The active lookup missed; a superseded memory (excluded from the active
		// index) is still readable, returned with a warning pointing at its
		// replacement so the agent reads the current knowledge instead.
		idx, found, err = s.resolveSupersededMemory(ctx, project, name)
		if err != nil {
			return errResult("memory_read", err)
		}
		if !found {
			return errResult("memory_read", fmt.Errorf("%w; check the name, pass project=<slug> or project=global, or use recall to search by text", scopedNotFound("memory", project, name)))
		}
	}
	mem, err := s.cfg.Files.Store().ReadMemory(idx.FilePath)
	if err != nil {
		return errResult("memory_read", err)
	}
	// Carry index-only lifecycle fields onto the file-parsed memory for the response.
	mem.InvalidAt, mem.SupersededBy = idx.InvalidAt, idx.SupersededBy
	s.record(ctx, core.EventMemoryRead, s.boundSession(ctx), mem.Project, mem.ID, map[string]any{"name": name})

	out := map[string]any{
		"id": mem.ID, "kind": string(mem.Kind), "name": mem.Name,
		"description": mem.Description, "project": mem.Project, "body": mem.Body,
		"tags": mem.Tags, "source_session": mem.SourceSession,
	}
	if !mem.Active() {
		out["warning"] = s.supersededWarning(ctx, mem)
	}
	return jsonResult(out)
}

// resolveSupersededMemory finds a superseded (invalid) memory by (project, name),
// falling back to the global scope, for memory_read's warning path.
func (s *Server) resolveSupersededMemory(ctx context.Context, project, name string) (core.Memory, bool, error) {
	m, ok, err := store.MemoryByNameIncludingInvalid(ctx, s.cfg.DB, project, name)
	if err != nil || ok {
		return m, ok, err
	}
	if project != "" {
		return store.MemoryByNameIncludingInvalid(ctx, s.cfg.DB, "", name)
	}
	return core.Memory{}, false, nil
}

// supersededWarning renders the read warning for an invalid memory, naming the
// replacement when superseded_by resolves to a known memory.
func (s *Server) supersededWarning(ctx context.Context, mem core.Memory) string {
	when := ""
	if mem.InvalidAt != nil {
		when = " on " + mem.InvalidAt.Format("2006-01-02")
	}
	if mem.SupersededBy != "" {
		if repl, ok, err := store.MemoryByID(ctx, s.cfg.DB, mem.SupersededBy); err == nil && ok {
			return fmt.Sprintf("superseded by %s%s; read that instead",
				lifecycle.MemoryRef(repl.Project, repl.Name), when)
		}
		return fmt.Sprintf("superseded by %s%s; read that instead", mem.SupersededBy, when)
	}
	return fmt.Sprintf("archived%s; this memory is no longer active", when)
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
	project, err := s.resolveReadScope(ctx, argString(req, "project"))
	if err != nil {
		return errResult("memory_delete", err)
	}
	idx, found, err := s.resolveMemory(ctx, project, name, true)
	if err != nil {
		return errResult("memory_delete", err)
	}
	if !found {
		return errResult("memory_delete", scopedNotFound("memory", project, name))
	}
	if err := s.cfg.Files.Remove(ctx, idx.FilePath); err != nil {
		return errResult("memory_delete", err)
	}
	s.record(ctx, core.EventMemoryArchived, s.boundSession(ctx), idx.Project, idx.ID, map[string]any{"name": name})
	// project names the scope actually deleted from: the lookup falls back to the
	// global scope, so a delete aimed at a project can land on a global memory --
	// the response must say so rather than implying the project-scoped one.
	return jsonResult(map[string]any{"status": "deleted", "id": idx.ID, "name": name, "project": idx.Project})
}

// scopedNotFound builds a "no such item" error that names the scope searched, so
// the agent can tell a wrong-project mistake from a wrong-name one.
func scopedNotFound(kind, project, name string) error {
	if project == "" {
		return fmt.Errorf("no %s named %q in the global scope", kind, name)
	}
	return fmt.Errorf("no %s named %q in project %q (also searched global)", kind, name, project)
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
