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
func (h *Handler) loadNoteBySlug(ctx context.Context, project, slug string) (core.Note, bool) {
	idx, ok, err := store.NoteBySlug(ctx, h.db, project, slug)
	if err != nil {
		h.logger.Warn("hooks: note lookup", "slug", slug, "error", err)
		return core.Note{}, false
	}
	if !ok {
		return core.Note{}, false
	}
	note, err := h.files.Store().ReadNote(idx.FilePath)
	if err != nil {
		h.logger.Warn("hooks: note read", "slug", slug, "error", err)
		return core.Note{}, false
	}
	return note, true
}
