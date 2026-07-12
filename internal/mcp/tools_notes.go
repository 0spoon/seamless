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
	"github.com/0spoon/seamless/internal/store"
)

func notesCreateTool() mcp.Tool {
	return mcp.NewTool("notes_create",
		mcp.WithDescription("Create a work note (research finding, decision record, summary). Auto-tagged created-by:agent."),
		mcp.WithString("title", mcp.Required(), mcp.Description("note title")),
		mcp.WithString("body", mcp.Required(), mcp.Description("markdown body (aliases: content, text)")),
		mcp.WithString("description", mcp.Description("optional one-line summary")),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound/ambient session's project. Pass project=global for a global (inbox) note. With no session and no explicit project the create is rejected as ambiguous.")),
		mcp.WithString("tags", mcp.Description("comma-separated tags")),
		mcp.WithString("source_url", mcp.Description("optional source URL")),
	)
}

func (s *Server) handleNotesCreate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	title := argString(req, "title")
	body := argBody(req)
	if title == "" || strings.TrimSpace(body) == "" {
		return errResult("notes_create", errors.New("title and body are required"))
	}
	project, err := s.resolveWriteScope(ctx, argString(req, "project"))
	if err != nil {
		return errResult("notes_create", err)
	}
	id, err := core.NewID()
	if err != nil {
		return errResult("notes_create", err)
	}
	now := time.Now().UTC()
	note := core.Note{
		ID: id, Title: title, Slug: slugify(title), Description: argString(req, "description"),
		Project: project, Body: body, Tags: appendUnique(argTags(req, "tags"), "created-by:agent"),
		SourceURL: argString(req, "source_url"), Created: now, Updated: now,
	}
	written, err := s.cfg.Files.WriteNote(ctx, note)
	if err != nil {
		return errResult("notes_create", err)
	}
	s.record(ctx, core.EventNoteWritten, s.boundSession(ctx), project, written.ID, map[string]any{"title": title})
	return jsonResult(map[string]any{"id": written.ID, "slug": written.Slug, "title": title, "project": project})
}

func notesReadTool() mcp.Tool {
	return mcp.NewTool("notes_read",
		mcp.WithDescription("Read a note by id."),
		mcp.WithString("id", mcp.Required(), mcp.Description("note id (ULID)")),
	)
}

func (s *Server) handleNotesRead(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	note, err := s.loadNote(ctx, argString(req, "id"))
	if err != nil {
		return errResult("notes_read", err)
	}
	return jsonResult(map[string]any{
		"id": note.ID, "title": note.Title, "slug": note.Slug, "description": note.Description,
		"project": note.Project, "body": note.Body, "tags": note.Tags, "source_url": note.SourceURL,
	})
}

func notesUpdateTool() mcp.Tool {
	return mcp.NewTool("notes_update",
		mcp.WithDescription("Update a note's fields by id (title, description, body, project, tags). Omitted fields are untouched; tags replace all. The slug and id stay stable."),
		mcp.WithString("id", mcp.Required(), mcp.Description("note id (ULID)")),
		mcp.WithString("title", mcp.Description("new title")),
		mcp.WithString("description", mcp.Description("new description")),
		mcp.WithString("body", mcp.Description("new body (aliases: content, text)")),
		mcp.WithString("project", mcp.Description("new project slug (\"\" or \"global\" = inbox)")),
		mcp.WithString("tags", mcp.Description("comma-separated tags, replacing all")),
	)
}

func (s *Server) handleNotesUpdate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	note, err := s.loadNote(ctx, argString(req, "id"))
	if err != nil {
		return errResult("notes_update", err)
	}
	args := req.GetArguments()
	oldProject := note.Project
	changed := false
	if v, ok := args["title"].(string); ok {
		note.Title = strings.TrimSpace(v)
		changed = true
	}
	if v, ok := args["description"].(string); ok {
		note.Description = strings.TrimSpace(v)
		changed = true
	}
	if v, ok := firstStringArg(args, "body", "content", "text"); ok {
		note.Body = v
		changed = true
	}
	if v, ok := args["project"].(string); ok {
		note.Project = normalizeProject(strings.TrimSpace(v))
		changed = true
	}
	if v, ok := args["tags"].(string); ok {
		note.Tags = parseCommaList(v)
		changed = true
	}
	if !changed {
		return errResult("notes_update", errors.New("provide at least one field to update"))
	}
	note.Updated = time.Now().UTC()

	// The slug is id-addressed and stays stable; a project change moves the file,
	// so drop the old path first to avoid an orphan.
	if note.Project != oldProject {
		if err := s.cfg.Files.Remove(ctx, files.NoteRelPath(oldProject, note.Slug)); err != nil {
			return errResult("notes_update", err)
		}
	}
	written, err := s.cfg.Files.WriteNote(ctx, note)
	if err != nil {
		return errResult("notes_update", err)
	}
	return jsonResult(map[string]any{"id": written.ID, "title": written.Title})
}

func notesAppendTool() mcp.Tool {
	return mcp.NewTool("notes_append",
		mcp.WithDescription("Append a timestamped line to a note's body."),
		mcp.WithString("id", mcp.Required(), mcp.Description("note id (ULID)")),
		mcp.WithString("body", mcp.Required(), mcp.Description("text to append (aliases: content, text)")),
	)
}

func (s *Server) handleNotesAppend(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	text := argBody(req)
	if strings.TrimSpace(text) == "" {
		return errResult("notes_append", errors.New("a non-empty body is required (aliases: content, text)"))
	}
	note, err := s.loadNote(ctx, argString(req, "id"))
	if err != nil {
		return errResult("notes_append", err)
	}
	stamp := time.Now().UTC().Format("2006-01-02 15:04")
	note.Body = strings.TrimRight(note.Body, "\n") + "\n\n" + stamp + " -- " + text + "\n"
	note.Updated = time.Now().UTC()
	written, err := s.cfg.Files.WriteNote(ctx, note)
	if err != nil {
		return errResult("notes_append", err)
	}
	return jsonResult(map[string]any{"id": written.ID, "title": written.Title})
}

func notesDeleteTool() mcp.Tool {
	return mcp.NewTool("notes_delete",
		mcp.WithDescription("Delete a note by id (removes the file and its index)."),
		mcp.WithString("id", mcp.Required(), mcp.Description("note id (ULID)")),
	)
}

func (s *Server) handleNotesDelete(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := argString(req, "id")
	if id == "" {
		return errResult("notes_delete", errors.New("id is required"))
	}
	idx, ok, err := store.NoteByID(ctx, s.cfg.DB, id)
	if err != nil {
		return errResult("notes_delete", err)
	}
	if !ok {
		return errResult("notes_delete", fmt.Errorf("no note with id %q", id))
	}
	if err := s.cfg.Files.Remove(ctx, idx.FilePath); err != nil {
		return errResult("notes_delete", err)
	}
	return jsonResult(map[string]any{"status": "deleted", "id": id})
}

// loadNote resolves a note id to its full on-disk content.
func (s *Server) loadNote(ctx context.Context, id string) (core.Note, error) {
	if id == "" {
		return core.Note{}, errors.New("id is required")
	}
	idx, ok, err := store.NoteByID(ctx, s.cfg.DB, id)
	if err != nil {
		return core.Note{}, err
	}
	if !ok {
		return core.Note{}, fmt.Errorf("no note with id %q", id)
	}
	return s.cfg.Files.Store().ReadNote(idx.FilePath)
}
