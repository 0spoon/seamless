package hooks

// Project and note resolution shared by the plan and subagent capture paths.

import (
	"context"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// resolveProject maps the hook payload cwd to a project slug (best-effort; ""
// scopes globally).
func (h *Handler) resolveProject(ctx context.Context, cwd string) string {
	project, err := store.ResolveProjectForCWD(ctx, h.db, cwd)
	if err != nil {
		h.logger.Warn("hooks: plan project resolve", "error", err)
		return ""
	}
	return project
}

// loadNoteBySlug resolves a (project, slug) to the full on-disk note.
//
// The read takes no lock of its own, so a caller that goes on to WRITE the note
// back must call this INSIDE the file's mutation lock (upsertPlanNote is the
// model: h.files.Mutate over files.NoteRelPath(project, slug), which is the same
// path the index row carries). Every capture path re-renders the whole note
// file, so a read taken before the lock builds its write from content another
// writer has already replaced -- and the loser's write vanishes with no error
// anywhere, because the index upsert is keyed by id.
func (h *Handler) loadNoteBySlug(ctx context.Context, project, slug string) (core.Note, bool) {
	path, ok := h.noteFileBySlug(ctx, project, slug)
	if !ok {
		return core.Note{}, false
	}
	note, err := h.files.Store().ReadNote(path)
	if err != nil {
		h.logger.Warn("hooks: note read", "slug", slug, "error", err)
		return core.Note{}, false
	}
	return note, true
}

// noteFileBySlug resolves a (project, slug) to the note's file path without
// reading it: a mutating caller needs the path before it can take the lock the
// read belongs under, and a note with no index row is "not here" rather than an
// error, exactly as loadNoteBySlug treats it.
func (h *Handler) noteFileBySlug(ctx context.Context, project, slug string) (string, bool) {
	idx, ok, err := store.NoteBySlug(ctx, h.db, project, slug)
	if err != nil {
		h.logger.Warn("hooks: note lookup", "slug", slug, "error", err)
		return "", false
	}
	if !ok {
		return "", false
	}
	return idx.FilePath, true
}
