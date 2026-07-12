package mcp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

func sessionStartTool() mcp.Tool {
	return mcp.NewTool("session_start",
		mcp.WithDescription("Begin or resume an agent work session and bind it to this connection. Returns the project briefing. Later memory/recall/notes calls inherit this session's project scope, so you rarely pass project again."),
		mcp.WithString("name", mcp.Description("Optional stable session name; reusing a name resumes that session")),
		mcp.WithString("cwd", mcp.Description("Absolute working directory; resolved to a project via the repo map")),
		mcp.WithString("source", mcp.Description("startup|resume|clear|compact|explicit")),
	)
}

func (s *Server) handleSessionStart(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req, "name")
	cwd := argString(req, "cwd")
	source := argString(req, "source")
	if source == "" {
		source = "explicit"
	}
	// Resolve the cwd to a project, registering a new repo->project mapping and
	// projects-table row when the agent works in a not-yet-mapped git repo.
	project, err := store.RegisterProjectForCWD(ctx, s.cfg.DB, cwd)
	if err != nil {
		return errResult("session_start", err)
	}

	// Resume a named session if it already exists.
	if name != "" {
		existing, ok, err := store.SessionByName(ctx, s.cfg.DB, name)
		if err != nil {
			return errResult("session_start", err)
		}
		if ok {
			if project == "" {
				project = existing.ProjectSlug
			}
			s.setBinding(ctx, existing.ID, project)
			s.record(ctx, core.EventSessionStarted, existing.ID, project, "", map[string]any{"resumed": true})
			briefing, _ := s.cfg.Retrieve.Briefing(ctx, retrieve.BriefingInput{CWD: cwd, Source: source})
			return jsonResult(map[string]any{
				"session_id": existing.ID, "name": existing.Name,
				"project": project, "resumed": true, "briefing": briefing,
			})
		}
	}

	id, err := core.NewID()
	if err != nil {
		return errResult("session_start", err)
	}
	if name == "" {
		name = "sess/" + shortID(id)
	}
	now := time.Now().UTC()
	sess := core.Session{
		ID: id, Name: name, ProjectSlug: project, Status: core.SessionActive,
		CWD: cwd, Source: source, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateSession(ctx, s.cfg.DB, sess); err != nil {
		return errResult("session_start", err)
	}
	s.setBinding(ctx, id, project)
	s.record(ctx, core.EventSessionStarted, id, project, "", nil)
	briefing, _ := s.cfg.Retrieve.Briefing(ctx, retrieve.BriefingInput{CWD: cwd, Source: source})
	return jsonResult(map[string]any{
		"session_id": id, "name": name, "project": project, "briefing": briefing,
	})
}

func sessionUpdateTool() mcp.Tool {
	return mcp.NewTool("session_update",
		mcp.WithDescription("Record interim progress on the current session (working findings so far). Uses the bound session unless you pass one."),
		mcp.WithString("findings", mcp.Required(), mcp.Description("Working findings / progress note so far")),
		mcp.WithString("session", mcp.Description("Optional session name; defaults to the bound session")),
	)
}

func (s *Server) handleSessionUpdate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	findings := argString(req, "findings")
	if findings == "" {
		return errResult("session_update", errors.New("findings is required"))
	}
	sess, ok, err := s.resolveSession(ctx, req)
	if err != nil {
		return errResult("session_update", err)
	}
	if !ok {
		return errResult("session_update", errNoSession)
	}
	sess.Findings = findings
	sess.UpdatedAt = time.Now().UTC()
	if err := store.UpdateSession(ctx, s.cfg.DB, sess); err != nil {
		return errResult("session_update", err)
	}
	return jsonResult(map[string]any{"session_id": sess.ID, "status": string(sess.Status)})
}

func sessionEndTool() mcp.Tool {
	return mcp.NewTool("session_end",
		mcp.WithDescription("Complete the current session, persisting its findings for future briefings. Uses the bound session unless you pass one."),
		mcp.WithString("findings", mcp.Required(), mcp.Description("Final findings: what was learned, decided, or left open. Prefer a tight summary (briefings show a short preview), but long findings are stored in full -- they are not rejected.")),
		mcp.WithString("session", mcp.Description("Optional session name; defaults to the bound session")),
	)
}

func (s *Server) handleSessionEnd(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	findings := argString(req, "findings")
	if findings == "" {
		return errResult("session_end", errors.New("findings is required and must not be empty"))
	}
	sess, ok, err := s.resolveSession(ctx, req)
	if err != nil {
		return errResult("session_end", err)
	}
	if !ok {
		return errResult("session_end", errNoSession)
	}
	sess.Status = core.SessionCompleted
	sess.Findings = findings
	sess.UpdatedAt = time.Now().UTC()
	if err := store.UpdateSession(ctx, s.cfg.DB, sess); err != nil {
		return errResult("session_end", err)
	}
	s.record(ctx, core.EventSessionEnded, sess.ID, sess.ProjectSlug, "", nil)
	return jsonResult(map[string]any{"status": "completed", "session_id": sess.ID})
}

// resolveSession loads the session named in the request, or the bound session
// when none is named.
func (s *Server) resolveSession(ctx context.Context, req mcp.CallToolRequest) (core.Session, bool, error) {
	if name := argString(req, "session"); name != "" {
		return store.SessionByName(ctx, s.cfg.DB, name)
	}
	id := s.boundSession(ctx)
	if id == "" {
		return core.Session{}, false, nil
	}
	return store.SessionByID(ctx, s.cfg.DB, id)
}

// shortID returns the last 8 characters of a ULID, lowercased, for a readable
// generated session name.
func shortID(id string) string {
	if len(id) <= 8 {
		return strings.ToLower(id)
	}
	return strings.ToLower(id[len(id)-8:])
}
