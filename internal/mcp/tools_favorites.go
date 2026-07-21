package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/store"
)

// favoriteKinds lists every entity kind favorite_set accepts. Plans resolve to
// their primary note (a plan is a composition, not a record); the rest map to
// their own store row or file.
var favoriteKinds = []string{"memory", "note", "project", "plan", "task", "session", "trial"}

func favoriteSetTool() mcp.Tool {
	return mcp.NewTool("favorite_set",
		mcp.WithDescription("Star or unstar an item. Favorites sort first in the console, are pinned "+
			"into session briefings (memories), and get a mild recall rank boost. For memories and notes "+
			"the flag is stored in the file's frontmatter; starring never bumps an item's updated time."),
		mcp.WithString("kind", mcp.Required(), enumOf(favoriteKinds),
			mcp.Description("what kind of item to star")),
		mcp.WithString("id", mcp.Required(),
			mcp.Description("the item's identifier: memory name, note id (or slug), project slug, "+
				"plan slug, task id, session id (or name), trial id")),
		mcp.WithBoolean("favorite", mcp.Required(),
			mcp.Description("true to star, false to unstar")),
		mcp.WithString("project", mcp.Description("project scope for memory-name/note-slug resolution; "+
			"defaults to the bound session's project")),
	)
}

func (s *Server) handleFavoriteSet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	kind := argString(req, "kind")
	id := argString(req, "id")
	fav := argBool(req, "favorite", false)
	if id == "" {
		return errResult("favorite_set", errors.New("id is required"))
	}

	var project, itemID string
	var err error
	switch kind {
	case "memory":
		project, itemID, err = s.setMemoryFavorite(ctx, req, id, fav)
	case "note", "plan":
		project, itemID, err = s.setNoteFavorite(ctx, req, kind, id, fav)
	case "project":
		_, found, perr := store.ProjectBySlug(ctx, s.cfg.DB, id)
		if perr == nil && !found {
			perr = fmt.Errorf("no project with slug %q", id)
		}
		if perr == nil {
			perr = store.SetProjectFavorite(ctx, s.cfg.DB, id, fav)
		}
		project, itemID, err = id, id, perr
	case "task":
		// No claim-lock check: a star is metadata, not a content mutation, so
		// starring a task another session holds is safe and allowed.
		t, terr := store.TaskByID(ctx, s.cfg.DB, id)
		if terr == nil {
			terr = store.SetTaskFavorite(ctx, s.cfg.DB, id, fav)
		}
		project, itemID, err = t.ProjectSlug, id, terr
	case "session":
		sess, found, serr := store.SessionByID(ctx, s.cfg.DB, id)
		if serr == nil && !found {
			sess, found, serr = store.SessionByName(ctx, s.cfg.DB, id)
		}
		if serr == nil && !found {
			serr = fmt.Errorf("no session with id or name %q", id)
		}
		if serr == nil {
			serr = store.SetSessionFavorite(ctx, s.cfg.DB, sess.ID, fav)
		}
		project, itemID, err = sess.ProjectSlug, sess.ID, serr
	case "trial":
		tr, found, terr := store.TrialByID(ctx, s.cfg.DB, id)
		if terr == nil && !found {
			terr = fmt.Errorf("no trial with id %q", id)
		}
		if terr == nil {
			terr = store.SetTrialFavorite(ctx, s.cfg.DB, id, fav)
		}
		project, itemID, err = tr.ProjectSlug, id, terr
	default:
		err = fmt.Errorf("unknown kind %q", kind)
	}
	if err != nil {
		return errResult("favorite_set", err)
	}

	s.record(ctx, core.EventFavoriteChanged, s.boundSession(ctx), project, itemID,
		map[string]any{"kind": kind, "id": id, "favorite": fav, "by": "agent"})
	return jsonResult(map[string]any{"kind": kind, "id": id, "favorite": fav})
}

// setMemoryFavorite resolves a memory by name (project scope + global fallback,
// like memory_read) and rewrites its file with the flag flipped. The full file
// is read first -- index rows carry no body, so writing from one would truncate
// the memory. Updated is deliberately not bumped: a star is not authorship.
func (s *Server) setMemoryFavorite(ctx context.Context, req mcp.CallToolRequest, id string, fav bool) (project, itemID string, err error) {
	name, err := memoryName(id)
	if err != nil {
		return "", "", err
	}
	scope, err := s.resolveReadScope(ctx, argString(req, "project"))
	if err != nil {
		return "", "", err
	}
	idx, found, err := s.resolveMemory(ctx, scope, name, true)
	if err != nil {
		return "", "", err
	}
	if !found {
		return "", "", scopedNotFound("memory", scope, name)
	}
	mem, err := s.cfg.Files.Store().ReadMemory(idx.FilePath)
	if err != nil {
		return "", "", err
	}
	if mem.Favorite != fav {
		mem.Favorite = fav
		if _, err := s.cfg.Files.WriteMemory(ctx, mem); err != nil {
			return "", "", err
		}
	}
	return mem.Project, mem.ID, nil
}

// setNoteFavorite flips the flag on a note (by id, falling back to slug in the
// session scope then global) or on a plan's primary note. A task-only plan has
// no note and cannot be starred.
func (s *Server) setNoteFavorite(ctx context.Context, req mcp.CallToolRequest, kind, id string, fav bool) (project, itemID string, err error) {
	var idx core.Note
	if kind == "plan" {
		primary, _, ok, cerr := plans.Composition(ctx, s.cfg.DB, id)
		if cerr != nil {
			return "", "", cerr
		}
		if !ok {
			return "", "", fmt.Errorf("plan %q has no note to favorite (task-only plans cannot be starred)", id)
		}
		idx = primary
	} else {
		var found bool
		idx, found, err = store.NoteByID(ctx, s.cfg.DB, id)
		if err != nil {
			return "", "", err
		}
		if !found {
			scope, serr := s.resolveReadScope(ctx, argString(req, "project"))
			if serr != nil {
				return "", "", serr
			}
			idx, found, err = store.NoteBySlug(ctx, s.cfg.DB, scope, id)
			if err == nil && !found && scope != "" {
				idx, found, err = store.NoteBySlug(ctx, s.cfg.DB, "", id)
			}
			if err != nil {
				return "", "", err
			}
			if !found {
				return "", "", scopedNotFound("note", scope, id)
			}
		}
	}
	note, err := s.cfg.Files.Store().ReadNote(idx.FilePath)
	if err != nil {
		return "", "", err
	}
	if note.Favorite != fav {
		note.Favorite = fav
		if _, err := s.cfg.Files.WriteNote(ctx, note); err != nil {
			return "", "", err
		}
	}
	return note.Project, note.ID, nil
}
