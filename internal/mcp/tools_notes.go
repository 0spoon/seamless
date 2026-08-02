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
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/store"
	"github.com/0spoon/seamless/internal/validate"
)

func notesCreateTool() mcp.Tool {
	return mcp.NewTool("notes_create", hintAdd(),
		mcp.WithDescription("Create a work note -- a research finding, decision record, meeting summary, or any artifact long enough to deserve its own file. Auto-tagged created-by:agent. Notes carry the long form; memory_write carries the compact durable knowledge a future session must not miss, so put the write-up here and the one-line lesson there rather than duplicating either. Pass plan=<slug> when the note is a plan's narrative or supporting context, so it joins that plan's composition beside its tasks. Do not use this for what the repo, AGENTS.md/CLAUDE.md, or the current conversation already records, and use notes_append to extend an existing note rather than creating a near-duplicate of it."),
		mcp.WithString("title", mcp.Required(), mcp.Description("note title")),
		mcp.WithString("body", mcp.Required(), mcp.Description("markdown body (aliases: content, text)")),
		mcp.WithString("description", mcp.Description("optional one-line summary")),
		mcp.WithString("project", mcp.Description(writeProjectArgDesc)),
		mcp.WithArray("tags", mcp.WithStringItems(), mcp.Description("tags (a comma-separated string is also accepted)")),
		mcp.WithString("plan", mcp.Description("optional plan slug (plan:<slug> convention): tags this note into that plan's composition so it surfaces on the Plans screen alongside its tasks_add plan=<slug> steps. Use it whenever this note is a plan's narrative or supporting context.")),
		mcp.WithString("source_url", mcp.Description("optional source URL")),
	)
}

func (s *Server) handleNotesCreate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	title := argString(req, "title")
	body := argRaw(req, "body")
	if title == "" || strings.TrimSpace(body) == "" {
		return errResult("notes_create", errors.New("title and body are required"))
	}
	if err := validate.Title(title); err != nil {
		return errResult("notes_create", err)
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
	tags := appendUnique(argStrings(req, "tags"), "created-by:agent")
	// A plan slug carries the note into the plan:<slug> composition -- the same
	// key tasks_add plan= writes -- so agents attach a plan's narrative without
	// having to hand-type the tag prefix.
	if plan := argString(req, "plan"); plan != "" {
		tags = appendUnique(tags, plans.SlugTag(plan))
	}
	note := core.Note{
		ID: id, Title: title, Slug: core.Slugify(title), Description: argString(req, "description"),
		Project: project, Body: body, Tags: tags,
		SourceURL: argString(req, "source_url"), Model: s.boundSessionModel(ctx),
		Created: now, Updated: now,
	}
	written, err := s.cfg.Files.WriteNote(ctx, note)
	if err != nil {
		return errResult("notes_create", err)
	}
	s.record(ctx, core.EventNoteWritten, s.boundSession(ctx), project, written.ID, map[string]any{"title": title})
	out := map[string]any{"id": written.ID, "slug": written.Slug, "title": title, "project": project}
	if plan := plans.SlugFromTags(written.Tags); plan != "" {
		out["plan"] = plan
	}
	return jsonResult(out)
}

func notesReadTool() mcp.Tool {
	return mcp.NewTool("notes_read", hintRead(),
		mcp.WithDescription("Read a note by id, or by slug within the current project (falling back to a global note of the same slug)."),
		mcp.WithString("id", mcp.Description("note id (ULID); pass exactly one of id or slug")),
		mcp.WithString("slug", mcp.Description("note slug, as briefings, plan compositions, and notes_create responses name notes (alias: name)")),
		mcp.WithString("project", mcp.Description("project slug for the slug lookup; defaults to the bound session's project")),
	)
}

func (s *Server) handleNotesRead(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, slug := argString(req, "id"), argString(req, "slug")
	if id != "" && slug != "" {
		return errResult("notes_read", errors.New("pass exactly one of id or slug"))
	}
	var note core.Note
	var err error
	if slug != "" {
		note, err = s.loadNoteBySlug(ctx, req, slug)
	} else {
		if id == "" {
			return errResult("notes_read", errors.New("id or slug is required (notes_read reads one note by ULID or exact slug; to search text use recall)"))
		}
		note, err = s.loadNote(ctx, id)
		if err == nil {
			// The by-id path resolves no scope -- a note id is globally unique --
			// so the fence is applied after the load, on the project the loaded
			// note names. Nothing of it reaches the caller before that answer.
			err = s.fenceRead(ctx, note.Project)
		}
	}
	if err != nil {
		return errResult("notes_read", err)
	}
	out := map[string]any{
		"id": note.ID, "title": note.Title, "slug": note.Slug, "description": note.Description,
		"project": note.Project, "body": note.Body, "tags": note.Tags, "source_url": note.SourceURL,
	}
	if note.Model != "" {
		out["model"] = note.Model
	}
	if note.Favorite {
		out["favorite"] = true
	}
	return jsonResult(out)
}

func notesUpdateTool() mcp.Tool {
	return mcp.NewTool("notes_update", hintOverwrite(),
		mcp.WithDescription("Update a note's fields by id (title, description, body, project, tags). Omitted fields are untouched; tags replace all. The slug and id stay stable."),
		mcp.WithString("id", mcp.Required(), mcp.Description("note id (ULID)")),
		mcp.WithString("title", mcp.Description("new title")),
		mcp.WithString("description", mcp.Description("new description")),
		mcp.WithString("body", mcp.Description("new body (aliases: content, text)")),
		mcp.WithString("project", mcp.Description("new project slug (\"\" or \"global\" = global scope)")),
		mcp.WithArray("tags", mcp.WithStringItems(), mcp.Description("tags, replacing all (a comma-separated string is also accepted); an empty list is read as absent and leaves the tags untouched")),
	)
}

func (s *Server) handleNotesUpdate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	note, err := s.loadNote(ctx, argString(req, "id"))
	if err != nil {
		return errResult("notes_update", err)
	}
	// A note id is globally unique, so nothing here resolved a write scope: the
	// fence is applied post-load, on the project the loaded note names. Without
	// it any caller holding a ULID edits a sealed project's note.
	if err := s.fenceWrite(ctx, note.Project); err != nil {
		return errResult("notes_update", err)
	}
	oldProject := note.Project
	changed := false
	if argPresent(req, "title") {
		title := argString(req, "title")
		if err := validate.Title(title); err != nil {
			return errResult("notes_update", err)
		}
		note.Title = title
		changed = true
	}
	if argPresent(req, "description") {
		note.Description = argString(req, "description")
		changed = true
	}
	if argPresent(req, "body") {
		note.Body = argRaw(req, "body")
		// A replaced body is new content: re-attribute it to the current model
		// when known (an unknown model keeps the prior attribution). Metadata-only
		// updates leave the producer untouched.
		if model := s.boundSessionModel(ctx); model != "" {
			note.Model = model
		}
		changed = true
	}
	if argPresent(req, "project") {
		project, perr := validateProjectArg(argString(req, "project"))
		if perr != nil {
			return errResult("notes_update", perr)
		}
		note.Project = project
		changed = true
	}
	if argPresent(req, "tags") {
		note.Tags = argStrings(req, "tags")
		changed = true
	}
	if !changed {
		return errResult("notes_update", errors.New("provide at least one field to update"))
	}
	note.Updated = time.Now().UTC()

	// The slug is id-addressed and stays stable; a project change moves the file.
	// Refuse when a different note already owns the target path (the UNIQUE
	// file_path index would reject it after the file was already clobbered).
	if note.Project != oldProject {
		// A move is a read of the old project plus a write into the new one, so it
		// needs both answers rather than the edit fence alone. That is what keeps
		// the inbound asymmetry from becoming an exit: an outside caller may edit a
		// confidential note in place (inbound is deliberately open), but carrying
		// it out of the fence is precisely the outbound leak confidential exists to
		// stop.
		if err := s.fenceRead(ctx, oldProject); err != nil {
			return errResult("notes_update", err)
		}
		if err := s.fenceWrite(ctx, note.Project); err != nil {
			return errResult("notes_update", err)
		}
		if other, ok, oerr := store.NoteBySlug(ctx, s.cfg.DB, note.Project, note.Slug); oerr != nil {
			return errResult("notes_update", oerr)
		} else if ok && other.ID != note.ID {
			return errResult("notes_update",
				fmt.Errorf("a different note with slug %q already exists in project %q", note.Slug, note.Project))
		}
	}
	// Write the new file BEFORE removing the old one: the index row is keyed by
	// id (the write repoints its file_path), so a failed write leaves the note
	// intact at its old path instead of deleting it outright.
	written, err := s.cfg.Files.WriteNote(ctx, note)
	if err != nil {
		return errResult("notes_update", err)
	}
	if note.Project != oldProject {
		if err := s.cfg.Files.Remove(ctx, files.NoteRelPath(oldProject, note.Slug)); err != nil {
			return errResult("notes_update", err)
		}
	}
	// A permitted write into a confidential project must not answer with that
	// project's text. The title leaks less than a task body does, but it is the
	// same shape: an outside caller holding a ULID reading authored content
	// through an edit it is entitled to make. Judged against the project the note
	// now sits in, so a move INTO the fence lands under it immediately.
	return jsonResult(s.withholdContent(ctx,
		note.Project, map[string]any{"id": written.ID, "title": written.Title}, noteContentFields))
}

func notesAppendTool() mcp.Tool {
	return mcp.NewTool("notes_append", hintAdd(),
		mcp.WithDescription("Append a UTC-timestamped line to an existing note's body, by id. Use it when a note is already the right home for what you learned -- a running investigation log, a decision record gaining one more data point -- so the note keeps its id, slug, and place in any plan composition instead of fragmenting into near-duplicates. Appending only ever adds: use notes_update to correct or restructure what is already there, and notes_create when the finding deserves an artifact of its own. Needs the note's id (ULID), which notes_create returns and briefings and plan compositions carry; notes_read resolves one from a slug."),
		mcp.WithString("id", mcp.Required(), mcp.Description("note id (ULID)")),
		mcp.WithString("body", mcp.Required(), mcp.Description("text to append (aliases: content, text)")),
	)
}

func (s *Server) handleNotesAppend(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	text := argRaw(req, "body")
	if strings.TrimSpace(text) == "" {
		return errResult("notes_append", errors.New("a non-empty body is required (aliases: content, text)"))
	}
	note, err := s.loadNote(ctx, argString(req, "id"))
	if err != nil {
		return errResult("notes_append", err)
	}
	// loadNote is shared with the read path, so the fence lives here rather than
	// inside it: reads and writes cross the fence on different terms, and one
	// check in the loader could only encode one of them.
	if err := s.fenceWrite(ctx, note.Project); err != nil {
		return errResult("notes_append", err)
	}
	stamp := time.Now().UTC().Format("2006-01-02 15:04")
	note.Body = strings.TrimRight(note.Body, "\n") + "\n\n" + stamp + " -- " + text + "\n"
	note.Updated = time.Now().UTC()
	written, err := s.cfg.Files.WriteNote(ctx, note)
	if err != nil {
		return errResult("notes_append", err)
	}
	// The title is the note's, not the caller's: an append that echoed it back
	// would be notes_read by another name for anyone outside the fence.
	return jsonResult(s.withholdContent(ctx,
		note.Project, map[string]any{"id": written.ID, "title": written.Title}, noteContentFields))
}

func notesDeleteTool() mcp.Tool {
	return mcp.NewTool("notes_delete", hintOverwrite(),
		mcp.WithDescription("Delete a note by id: the markdown file leaves the disk and its index row goes with it. Permanent, and it leaves no pointer behind, so reserve it for notes that should never have existed -- a duplicate, a write into the wrong project, an agent's own scratch. To fix a note's content use notes_update, and to add to it notes_append; neither loses the artifact. A note tagged into a plan (plan:<slug>) is that plan's narrative for whoever inherits it, so read it with notes_read before deciding it is disposable."),
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
	// The most consequential of the three: a delete leaves no pointer behind, so
	// an unfenced by-id path let an outside caller destroy a sealed project's note.
	if err := s.fenceWrite(ctx, idx.Project); err != nil {
		return errResult("notes_delete", err)
	}
	if err := s.cfg.Files.Remove(ctx, idx.FilePath); err != nil {
		return errResult("notes_delete", err)
	}
	return jsonResult(map[string]any{"status": "deleted", "id": id})
}

// noteContentFields is the authored text notes_update and notes_append answer
// with, and therefore what withholdContent removes for a caller that may write
// the note's project but not read it. Only the id survives -- it is the caller's
// own handle, and without it the caller could not act on what it just wrote.
var noteContentFields = []string{"title"}

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
		return core.Note{}, fmt.Errorf("no note with id %q; read by slug=<slug>, or use recall to search by text", id)
	}
	return s.cfg.Files.Store().ReadNote(idx.FilePath)
}

// loadNoteBySlug resolves a note slug to its full on-disk content, searching the
// request's read scope first and falling back to the global scope -- the same
// resolution memory_read applies to names, because briefings, plan compositions,
// and notes_create responses all name notes by slug.
func (s *Server) loadNoteBySlug(ctx context.Context, req mcp.CallToolRequest, slug string) (core.Note, error) {
	slug = core.Slugify(slug)
	project, err := s.resolveReadScope(ctx, argString(req, "project"))
	if err != nil {
		return core.Note{}, err
	}
	idx, ok, err := store.NoteBySlug(ctx, s.cfg.DB, project, slug)
	if err != nil {
		return core.Note{}, err
	}
	if !ok && project != "" {
		// The fallback resolves no scope of its own, so a sealed session would
		// otherwise read a global note its fence had removed from its world.
		global, gerr := s.canReadGlobal(ctx)
		if gerr != nil {
			return core.Note{}, gerr
		}
		if global {
			idx, ok, err = store.NoteBySlug(ctx, s.cfg.DB, "", slug)
			if err != nil {
				return core.Note{}, err
			}
		}
	}
	if !ok {
		return core.Note{}, s.scopedNotFound(ctx, "note", project, slug, noteReadMissHelp, noteReadMissHelpSealed)
	}
	return s.cfg.Files.Store().ReadNote(idx.FilePath)
}

// The by-name miss tails for notes_read, paired like the memory ones: the plain
// form, and the same advice minus the global scope a sealed session cannot reach.
const (
	noteReadMissHelp       = "; check the slug, pass project=<slug> or project=global, read by id=<ULID>, or use recall to search by text"
	noteReadMissHelpSealed = "; check the slug, read by id=<ULID>, or use recall to search by text"
)
