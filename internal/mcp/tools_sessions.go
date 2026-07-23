package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

func sessionStartTool() mcp.Tool {
	return mcp.NewTool("session_start",
		mcp.WithDescription("Begin or resume an agent work session and bind it to this connection. Returns the project briefing. Later memory/recall/notes calls inherit this session's project scope, so you rarely pass project again."),
		mcp.WithString("name", mcp.Description("Optional stable session name; reusing a name resumes that session")),
		mcp.WithString("cwd", mcp.Description("Absolute working directory; auto-mapped to a project from the repo root on a repo's first session (no setup step -- `seamlessd map-repo` only overrides the derived slug)")),
		mcp.WithString("source", enumOf(core.SessionSources), mcp.Description("what began this session (default startup)")),
		mcp.WithString("model", mcp.Description("Model id powering this agent, exactly as the provider names it (e.g. claude-fable-5, gpt-5.5). Stamped onto memories/notes this session writes; hooks keep it current for Claude Code/Codex sessions, so pass it mainly from other clients")),
	)
}

func (s *Server) handleSessionStart(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req, "name")
	cwd := argString(req, "cwd")
	source := argString(req, "source")
	model := strings.TrimSpace(argString(req, "model"))
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
			// Reactivate: a resumed session is live again. Without this a
			// completed/expired session stays terminal -- the per-call heartbeat
			// (TouchSession) only touches active sessions, so the idle reaper and
			// every active-session surface would treat the resumed agent as gone.
			existing.Status = core.SessionActive
			existing.UpdatedAt = time.Now().UTC()
			if err := store.UpdateSession(ctx, s.cfg.DB, existing); err != nil {
				return errResult("session_start", err)
			}
			s.stampSessionModel(ctx, existing.ID, model)
			s.setBinding(ctx, existing.ID, project)
			s.record(ctx, core.EventSessionStarted, existing.ID, project, "", map[string]any{"resumed": true})
			return jsonResult(map[string]any{
				"session_id": existing.ID, "name": existing.Name,
				"project": project, "resumed": true, "scope": scopeNote(project),
				"briefing": s.briefing(ctx, cwd, source),
			})
		}
	}

	// Adopt the connection's ambient session: with no explicit name and exactly
	// one active ambient (cc/* or cx/*) session sharing the cwd, the SessionStart hook
	// already created this agent's session -- resume that row instead of minting
	// a second sess/* one. Zero or many candidates (no hook ran, or two agents in
	// one cwd) fall through to a fresh session, the same unambiguous-or-fallback
	// guard as linkedExternalIdentity, so adoption can never bind a sibling's session.
	if name == "" {
		if ambient, ok := s.soleAmbientByCWD(ctx, cwd); ok {
			if project == "" {
				project = ambient.ProjectSlug
			}
			ambient.ProjectSlug = project
			ambient.UpdatedAt = time.Now().UTC()
			if err := store.UpdateSession(ctx, s.cfg.DB, ambient); err != nil {
				return errResult("session_start", err)
			}
			s.stampSessionModel(ctx, ambient.ID, model)
			s.setBinding(ctx, ambient.ID, project)
			s.record(ctx, core.EventSessionStarted, ambient.ID, project, "",
				map[string]any{"resumed": true, "adopted": true})
			return jsonResult(map[string]any{
				"session_id": ambient.ID, "name": ambient.Name,
				"project": project, "resumed": true, "scope": scopeNote(project),
				"briefing": s.briefing(ctx, cwd, source),
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
	externalSessionID, externalClient := s.linkedExternalIdentity(ctx, cwd)
	sess := core.Session{
		ID: id, Name: name, ProjectSlug: project, Status: core.SessionActive,
		CWD: cwd, Source: source, Model: model,
		ExternalSessionID: externalSessionID, ExternalClient: externalClient,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateSession(ctx, s.cfg.DB, sess); err != nil {
		return errResult("session_start", err)
	}
	s.setBinding(ctx, id, project)
	s.record(ctx, core.EventSessionStarted, id, project, "", nil)
	return jsonResult(map[string]any{
		"session_id": id, "name": name, "project": project, "scope": scopeNote(project),
		"briefing": s.briefing(ctx, cwd, source),
	})
}

// stampSessionModel records a self-reported model id on a resumed/adopted
// session. Best-effort: attribution must never fail a session_start, so an
// error logs and the session simply keeps its previous (possibly empty) model.
// No-op on an empty model -- the hooks' sniffed value survives an agent that
// does not pass one.
func (s *Server) stampSessionModel(ctx context.Context, sessionID, model string) {
	if model == "" {
		return
	}
	if err := store.SetSessionModel(ctx, s.cfg.DB, sessionID, model); err != nil {
		s.logger.Warn("session_start: set model", "error", err)
	}
}

// scopeNote explains, in the session_start result, how project scope was
// resolved -- so an agent sees that the repo->project mapping is automatic and
// never mistakes `seamlessd map-repo` for a required setup step. The map grows
// itself on a repo's first session (see store.RegisterProjectForCWD); map-repo
// only overrides the derived slug.
func scopeNote(project string) string {
	if project == "" {
		return "global scope: this cwd is not inside a git repo, so nothing auto-mapped. " +
			"Pass project=<slug> on durable writes to target a project."
	}
	return fmt.Sprintf("project %q. Repo->project mapping is automatic -- a repo maps itself to a "+
		"project on its first session, so there is no setup step. `seamlessd map-repo` only "+
		"overrides a repo's derived slug.", project)
}

// briefing assembles the session_start briefing, degrading to "" on error. The
// failure is logged (it was previously discarded silently): a broken briefing
// should never fail a session_start, but it must not vanish without a trace.
func (s *Server) briefing(ctx context.Context, cwd, source string) string {
	briefing, _, err := s.cfg.Retrieve.Briefing(ctx, retrieve.BriefingInput{CWD: cwd, Source: source})
	if err != nil {
		s.logger.Warn("session_start: briefing", "error", err)
		return ""
	}
	return briefing
}

// linkedExternalIdentity resolves the client/id pair to stamp on a freshly
// created NAMED explicit session, so a graceful SessionEnd closes it alongside
// its ambient rather than leaving it for the idle reaper. (An unnamed
// session_start with a sole same-cwd ambient adopts that session outright and
// never gets here.) Ambiguity yields empty values so the session falls back to
// the reaper instead of risking a link to the wrong agent. Best-effort.
func (s *Server) linkedExternalIdentity(ctx context.Context, cwd string) (externalSessionID, externalClient string) {
	ambient, ok := s.soleAmbientByCWD(ctx, cwd)
	if !ok {
		return "", ""
	}
	return ambient.ExternalSessionID, ambient.ExternalClient
}

// soleAmbientByCWD returns the single active ambient (cc/* or cx/*) session sharing cwd --
// the unambiguous this-agent case. Zero or many candidates (no ambient yet, or two
// agents in one cwd) report ok=false so callers fall back to a fresh session rather
// than risking a cross-agent match. Best-effort: a lookup error logs and reports no
// match.
func (s *Server) soleAmbientByCWD(ctx context.Context, cwd string) (core.Session, bool) {
	ambients, err := store.ActiveAmbientByCWD(ctx, s.cfg.DB, cwd)
	if err != nil {
		s.logger.Warn("session_start: ambient lookup", "error", err)
		return core.Session{}, false
	}
	if len(ambients) != 1 {
		return core.Session{}, false
	}
	return ambients[0], true
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
		mcp.WithArray("mishaps", mcp.WithStringItems(), mcp.Description("Self-report mishaps this session caused: an action a warning or convention said not to take, live state touched by mistake, a command that hit the wrong target. Pass an array with one short entry per incident; omit when none happened. When a mishap violated a stored memory, name that memory by its exact slug in the entry (e.g. \"violated chroma-boot-race by ...\") -- the report is then linked to it. Recorded for recurrence review, not blame -- report them even when fully recovered.")),
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
	// The session is over: drop every connection binding pointing at it, so the
	// bindings map does not grow one dead entry per ended session on a
	// long-lived daemon (sessions ended by the hook or the reaper are swept
	// separately -- see maybeSweepBindings).
	s.evictSessionBindings(sess.ID)
	// Release any task claims this session still holds so its in-flight work
	// returns to the queue rather than sitting claimed by a departed agent.
	// Keyed off the resolved sess.ID (not the connection binding) because
	// session_end may complete an ambient session this connection isn't bound to.
	released, err := store.ReleaseClaimsForSession(ctx, s.cfg.DB, sess.ID, now)
	if err != nil {
		return errResult("session_end", err)
	}
	// Self-reported mishaps land as one durable agent.mishap event each -- the
	// only record of an action no telemetry can observe (the agent confessing it
	// is the signal). Recorded before session.ended so the session's timeline
	// reads in order. Each text is scanned once, here at ingestion, for active
	// memories it names (mishapMemoryIDs); matches persist in the payload as
	// item_ids, so nothing ever re-matches against a corpus that has since
	// changed. The linkage feeds the briefing's mishap promotion
	// (store.RecentMishapItemIDs) and deliberately NOT the utility score.
	mishaps := argStrings(req, "mishaps")
	corpus := s.mishapMatchCorpus(ctx, sess.ProjectSlug, len(mishaps))
	for _, m := range mishaps {
		payload := map[string]any{"description": events.Truncate(m, s.cfg.ToolEventMaxChars)}
		if ids := mishapMemoryIDs(m, corpus); len(ids) > 0 {
			payload["item_ids"] = ids
		}
		s.record(ctx, core.EventAgentMishap, sess.ID, sess.ProjectSlug, "", payload)
	}
	s.record(ctx, core.EventSessionEnded, sess.ID, sess.ProjectSlug, "",
		map[string]any{"claims_released": released, "findings": events.Truncate(findings, s.cfg.ToolEventMaxChars)})
	return jsonResult(map[string]any{"status": "completed", "session_id": sess.ID, "claims_released": released, "mishaps_recorded": len(mishaps)})
}

// mishapMatchCorpus loads the active memories a mishap report may name: the
// session project's own plus global scope, the same visibility rule every other
// read uses -- another project's memory never matches. Skipped entirely when
// there are no mishaps. Best-effort: linkage must never fail a session_end, so
// a load error logs and the reports come through unlinked.
func (s *Server) mishapMatchCorpus(ctx context.Context, project string, mishapCount int) []core.Memory {
	if mishapCount == 0 {
		return nil
	}
	mems, err := store.ActiveMemories(ctx, s.cfg.DB, project)
	if err != nil {
		s.logger.Warn("session_end: load memories for mishap linkage", "error", err)
		return nil
	}
	return mems
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
// session. Here more than one candidate -- across projects, or two agents' cc/* or
// cx/* ambients in one project -- yields ambiguous=true and no session, so the caller
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
