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
			briefing, _, _ := s.cfg.Retrieve.Briefing(ctx, retrieve.BriefingInput{CWD: cwd, Source: source})
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
	briefing, _, _ := s.cfg.Retrieve.Briefing(ctx, retrieve.BriefingInput{CWD: cwd, Source: source})
	return jsonResult(map[string]any{
		"session_id": id, "name": name, "project": project, "briefing": briefing,
	})
}

func sessionUpdateTool() mcp.Tool {
	return mcp.NewTool("session_update",
		mcp.WithDescription("Record interim progress on the current session (working findings so far). Uses the bound session unless you pass one."),
		mcp.WithString("findings", mcp.Required(), mcp.Description("Working findings / progress note so far")),
		mcp.WithString("session", mcp.Description("Optional session name; defaults to the bound session")),
		mcp.WithString("session_id", mcp.Description("Optional session ULID; takes precedence over session and the bound session")),
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
		mcp.WithString("session_id", mcp.Description("Optional session ULID; takes precedence over session and the bound session")),
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
	now := time.Now().UTC()
	sess.Status = core.SessionCompleted
	sess.Findings = findings
	sess.UpdatedAt = now
	if err := store.UpdateSession(ctx, s.cfg.DB, sess); err != nil {
		return errResult("session_end", err)
	}
	// Release any task claims this session still holds so its in-flight work
	// returns to the queue rather than sitting claimed by a departed agent.
	// Keyed off the resolved sess.ID (not the connection binding) because
	// session_end may complete an ambient session this connection isn't bound to.
	released, err := store.ReleaseClaimsForSession(ctx, s.cfg.DB, sess.ID, now)
	if err != nil {
		return errResult("session_end", err)
	}
	s.record(ctx, core.EventSessionEnded, sess.ID, sess.ProjectSlug, "",
		map[string]any{"claims_released": released})
	return jsonResult(map[string]any{"status": "completed", "session_id": sess.ID, "claims_released": released})
}

// resolveSession loads the session the request targets: an explicit session_id
// (ULID) first, then a session name, then the connection's bound session, and
// only then a single unambiguous active ambient session. Accepting an id as well
// as a name stops a session_id= argument from being silently ignored and dropping
// to the fallback -- the call-site mistake behind an overwrite of the wrong
// agent's session. The fallback is stricter than for reads/writes: it refuses
// (errAmbiguousSession) whenever more than one active ambient session could be the
// one meant -- including two agents' ambients in the SAME repo -- because
// completing the wrong session is destructive, not merely mis-scoped.
func (s *Server) resolveSession(ctx context.Context, req mcp.CallToolRequest) (core.Session, bool, error) {
	if id := argString(req, "session_id"); id != "" {
		return store.SessionByID(ctx, s.cfg.DB, id)
	}
	if name := argString(req, "session"); name != "" {
		return store.SessionByName(ctx, s.cfg.DB, name)
	}
	if b, ok := s.getBinding(ctx); ok {
		return store.SessionByID(ctx, s.cfg.DB, b.sessionID)
	}
	sess, ok, ambiguous, err := s.ambientSessionTarget(ctx)
	if err != nil {
		return core.Session{}, false, err
	}
	if ambiguous {
		return core.Session{}, false, errAmbiguousSession
	}
	return sess, ok, nil
}

// ambientSessionTarget resolves the single active ambient session an unbound
// session_update/end may target, or reports ambiguity. It is stricter than
// ambientFallback: that one collapses a project's ambients to the most recent for
// provenance, which is fine for stamping an event but not for *completing* a
// session. Here more than one candidate -- across projects, or two agents' cc/*
// ambients in one project -- yields ambiguous=true and no session, so the caller
// must name the session. Exactly one candidate (the solo-agent case) resolves.
func (s *Server) ambientSessionTarget(ctx context.Context) (sess core.Session, ok bool, ambiguous bool, err error) {
	projects, err := store.ActiveAmbientProjects(ctx, s.cfg.DB, ambientFallbackWindow)
	if err != nil {
		return core.Session{}, false, false, err
	}
	switch len(projects) {
	case 0:
		return core.Session{}, false, false, nil
	case 1:
		// Single project: still ambiguous if two agents left ambients in it.
	default:
		return core.Session{}, false, true, nil
	}
	sessions, err := store.ActiveAmbientSessionsForProject(ctx, s.cfg.DB, projects[0], ambientFallbackWindow)
	if err != nil {
		return core.Session{}, false, false, err
	}
	if len(sessions) != 1 {
		return core.Session{}, false, len(sessions) > 1, nil
	}
	return sessions[0], true, false, nil
}

// shortID returns the last 8 characters of a ULID, lowercased, for a readable
// generated session name.
func shortID(id string) string {
	if len(id) <= 8 {
		return strings.ToLower(id)
	}
	return strings.ToLower(id[len(id)-8:])
}
