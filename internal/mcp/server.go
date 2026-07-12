// Package mcp hosts the Seamless MCP tool surface over streamable HTTP with a
// single static bearer key. It adds what v1 lacked: a per-connection session
// binding, so an agent calls session_start once and later memory/recall/notes
// calls inherit the session's project scope without repeating it. Every stored
// identity is a ULID; names and slugs are only ergonomic handles.
package mcp

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/0spoon/seamless/internal/capture"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/gardener"
	"github.com/0spoon/seamless/internal/llm"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

const (
	serverName    = "Seamless"
	serverVersion = "0.0.0-dev"

	// ToolCount is the number of MCP tools registered. doctor asserts the actual
	// registered count (Server.NumTools) equals it. P2 minimal loop = 15; P3 adds
	// tasks (4) + trials (3) = 22; P4 adds gardener (2) + capture_url +
	// usage_summary = 26.
	ToolCount = 26

	// globalNamespace is the reserved project token an agent passes to
	// deliberately target the global (cross-project) scope, instead of relying on
	// an empty project (which is easy to hit by accident). "_global" -- the
	// on-disk directory name -- is accepted as a synonym.
	globalNamespace = "global"
)

// errNoSession is returned when a session-scoped tool is called with neither a
// bound session nor an explicit one.
var errNoSession = errors.New("no active session: call session_start first, or pass a session name")

// errNoScope is returned by resolveWriteScope for a durable create with no
// explicit project, no bound session, and no ambient session at all to inherit
// from -- so the write would silently land in the global scope. The agent must
// choose.
var errNoScope = errors.New(
	"ambiguous scope: no bound or ambient session to infer the project from; " +
		"pass project=<slug> for a project item, or project=global for a global one")

// errAmbiguousScope is returned when a durable create has no explicit project and
// no bound session, and active ambient sessions span multiple projects
// (concurrent agents in different repos). Inheriting the machine-latest ambient
// here is exactly how a write bleeds into another agent's project, so the caller
// must disambiguate with project=.
// errAmbiguousSession is returned by resolveSession when a session_update/end
// gives no explicit session_id or session name, has no bound session, and more
// than one active ambient session -- across projects, or two agents' cc/* ambients
// in the same repo -- could be the target. Guessing here would complete or
// overwrite another agent's session, so the caller must name it explicitly.
var errAmbiguousSession = errors.New(
	"ambiguous session: no bound session, and multiple active ambient sessions " +
		"could be the target; pass session_id=<ULID> or session=<name>")

var errAmbiguousScope = errors.New(
	"ambiguous scope: no bound session, and active ambient sessions span multiple " +
		"projects (concurrent agents); pass project=<slug> for a project item, or " +
		"project=global for a global one")

// normalizeProject maps the reserved global tokens to the internal empty-string
// global scope, and leaves every other slug untouched.
func normalizeProject(explicit string) string {
	switch explicit {
	case globalNamespace, "_global":
		return ""
	default:
		return explicit
	}
}

// Config wires the MCP server's dependencies.
type Config struct {
	DB       *sql.DB
	Files    *files.Manager
	Retrieve *retrieve.Service
	Events   *events.Recorder
	Gardener *gardener.Service // may be nil (gardener_apply is then unavailable)
	Embedder llm.Embedder      // may be nil (memory_write dedup hint is then skipped)
	APIKey   string
	Version  string // build version advertised in the MCP handshake; defaults to serverVersion
	Logger   *slog.Logger
}

// Server hosts the MCP tools and their per-connection session bindings.
type Server struct {
	mcp       *mcpserver.MCPServer
	cfg       Config
	logger    *slog.Logger
	fetcher   *capture.URLFetcher // SSRF-safe URL fetch backing capture_url
	toolNames []string            // registered tool names, in registration order

	mu       sync.Mutex
	bindings map[string]binding // mcp client-session id -> binding
}

// NumTools returns the number of registered MCP tools. doctor asserts it equals
// ToolCount, catching a tool that was written but never wired into registerTools.
func (s *Server) NumTools() int { return len(s.toolNames) }

// addTool registers one tool and records its name so NumTools stays accurate.
func (s *Server) addTool(t mcp.Tool, h mcpserver.ToolHandlerFunc) {
	s.toolNames = append(s.toolNames, t.Name)
	s.mcp.AddTool(t, h)
}

// binding is the session context inherited by later tool calls on a connection.
type binding struct {
	sessionID string // ULID of the bound session
	project   string // resolved project slug ("" = global)
	lab       string // current research lab (set by lab_open), inherited by trial_record
}

// New constructs a Server and registers the tool surface.
func New(cfg Config) *Server {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	version := cfg.Version
	if version == "" {
		version = serverVersion
	}
	s := &Server{
		cfg:      cfg,
		logger:   logger,
		fetcher:  capture.NewURLFetcher(),
		bindings: make(map[string]binding),
	}
	s.mcp = mcpserver.NewMCPServer(
		serverName, version,
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithRecovery(),
		mcpserver.WithToolHandlerMiddleware(s.authMiddleware),
	)
	s.registerTools()
	return s
}

// Handler returns the streamable-HTTP handler for /api/mcp. The static key is
// verified in the HTTP context func (which tags the context on success); the
// tool middleware rejects any call whose context was not tagged.
func (s *Server) Handler() http.Handler {
	return mcpserver.NewStreamableHTTPServer(s.mcp,
		mcpserver.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			if verifyBearer(r, s.cfg.APIKey) {
				ctx = context.WithValue(ctx, authedKey{}, true)
			}
			return ctx
		}),
	)
}

func (s *Server) registerTools() {
	s.addTool(sessionStartTool(), s.handleSessionStart)
	s.addTool(sessionUpdateTool(), s.handleSessionUpdate)
	s.addTool(sessionEndTool(), s.handleSessionEnd)

	s.addTool(memoryWriteTool(), s.handleMemoryWrite)
	s.addTool(memoryAppendTool(), s.handleMemoryAppend)
	s.addTool(memoryReadTool(), s.handleMemoryRead)
	s.addTool(memoryDeleteTool(), s.handleMemoryDelete)

	s.addTool(recallTool(), s.handleRecall)

	s.addTool(notesCreateTool(), s.handleNotesCreate)
	s.addTool(notesReadTool(), s.handleNotesRead)
	s.addTool(notesUpdateTool(), s.handleNotesUpdate)
	s.addTool(notesAppendTool(), s.handleNotesAppend)
	s.addTool(notesDeleteTool(), s.handleNotesDelete)

	s.addTool(projectListTool(), s.handleProjectList)
	s.addTool(projectCreateTool(), s.handleProjectCreate)

	s.addTool(tasksAddTool(), s.handleTasksAdd)
	s.addTool(tasksUpdateTool(), s.handleTasksUpdate)
	s.addTool(tasksReadyTool(), s.handleTasksReady)
	s.addTool(tasksListTool(), s.handleTasksList)

	s.addTool(labOpenTool(), s.handleLabOpen)
	s.addTool(trialRecordTool(), s.handleTrialRecord)
	s.addTool(trialQueryTool(), s.handleTrialQuery)

	s.addTool(gardenerProposalsTool(), s.handleGardenerProposals)
	s.addTool(gardenerApplyTool(), s.handleGardenerApply)

	s.addTool(captureURLTool(), s.handleCaptureURL)
	s.addTool(usageSummaryTool(), s.handleUsageSummary)
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

type authedKey struct{}

// authMiddleware rejects any tool call whose HTTP request did not present the
// valid static key (tool errors are returned as results, not Go errors).
func (s *Server) authMiddleware(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if v, _ := ctx.Value(authedKey{}).(bool); !v {
			return mcp.NewToolResultError("unauthorized: valid bearer key required"), nil
		}
		return next(ctx, req)
	}
}

// verifyBearer constant-time-compares the request's bearer token to key. An
// empty configured key rejects everything.
func verifyBearer(r *http.Request, key string) bool {
	if key == "" {
		return false
	}
	parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(parts[1]), []byte(key)) == 1
}

// ---------------------------------------------------------------------------
// Session binding
// ---------------------------------------------------------------------------

// mcpSessionID returns the streamable-HTTP client session id, or "" if the
// transport is stateless (in which case tools fall back to explicit args).
func (s *Server) mcpSessionID(ctx context.Context) string {
	if cs := mcpserver.ClientSessionFromContext(ctx); cs != nil {
		return cs.SessionID()
	}
	return ""
}

func (s *Server) setBinding(ctx context.Context, sessionID, project string) {
	id := s.mcpSessionID(ctx)
	if id == "" {
		return
	}
	s.mu.Lock()
	s.bindings[id] = binding{sessionID: sessionID, project: project}
	s.mu.Unlock()
}

func (s *Server) getBinding(ctx context.Context) (binding, bool) {
	id := s.mcpSessionID(ctx)
	if id == "" {
		return binding{}, false
	}
	s.mu.Lock()
	b, ok := s.bindings[id]
	s.mu.Unlock()
	return b, ok
}

// ambientFallbackWindow bounds how stale an ambient (cc/*) session may be and
// still absorb writes from an unbound connection. On a single-owner machine the
// ambiguity window is acceptable; a shorter window would drop provenance for a
// long-running session that has gone quiet.
const ambientFallbackWindow = 6 * time.Hour

// resolveReadScope resolves the project for a read or by-name lookup (recall,
// memory_read/append/delete, tasks_list/ready). It prefers an explicit project
// (global token normalized), then the bound session's project, then a single
// unambiguous ambient project. It differs from resolveWriteScope only in the
// empty case: a read with nothing to infer from legitimately targets the global
// scope, so it returns ("", nil) there rather than errNoScope. It still refuses
// with errAmbiguousScope when active ambient sessions span multiple projects --
// otherwise it would silently narrow the read to global and hide the intended
// project's items, the read-side twin of the cross-agent write bleed.
func (s *Server) resolveReadScope(ctx context.Context, explicit string) (string, error) {
	if explicit != "" {
		return normalizeProject(explicit), nil
	}
	if b, ok := s.getBinding(ctx); ok {
		return b.project, nil
	}
	sess, ok, ambiguous := s.ambientFallback(ctx)
	if ok {
		return sess.ProjectSlug, nil
	}
	if ambiguous {
		return "", errAmbiguousScope
	}
	return "", nil
}

// resolveWriteScope is resolveReadScope for durable creates (memory/note/task/
// captured-note/trial): it returns the same project, but errors with errNoScope
// instead of defaulting to global when nothing pins the scope. An explicit project
// -- including project=global -- is always unambiguous, as is any bound session
// or a single-project ambient fallback. When active ambient sessions span
// multiple projects (concurrent agents), it refuses to guess rather than bleed
// the write into the wrong project.
func (s *Server) resolveWriteScope(ctx context.Context, explicit string) (string, error) {
	if explicit != "" {
		return normalizeProject(explicit), nil
	}
	if b, ok := s.getBinding(ctx); ok {
		return b.project, nil
	}
	sess, ok, ambiguous := s.ambientFallback(ctx)
	if ok {
		return sess.ProjectSlug, nil
	}
	if ambiguous {
		return "", errAmbiguousScope
	}
	return "", errNoScope
}

// setBindingLab records the current research lab on the connection's binding, so
// a later trial_record can inherit it. It is a no-op on a stateless transport
// (no client session id) -- such callers pass lab explicitly.
func (s *Server) setBindingLab(ctx context.Context, lab string) {
	id := s.mcpSessionID(ctx)
	if id == "" {
		return
	}
	s.mu.Lock()
	b := s.bindings[id]
	b.lab = lab
	s.bindings[id] = b
	s.mu.Unlock()
}

// boundLab returns the connection's current lab, or "".
func (s *Server) boundLab(ctx context.Context) string {
	if b, ok := s.getBinding(ctx); ok {
		return b.lab
	}
	return ""
}

// boundSession returns the bound session's ULID, a single unambiguous ambient
// session's ULID (write-scope fallback), or "".
func (s *Server) boundSession(ctx context.Context) string {
	if b, ok := s.getBinding(ctx); ok {
		return b.sessionID
	}
	if sess, ok, _ := s.ambientFallback(ctx); ok {
		return sess.ID
	}
	return ""
}

// ambientFallback returns the ambient session an unbound connection's writes
// should attribute to. Because such a connection carries no project signal (MCP
// tool calls receive no cwd, and no session_start ran), attribution is only safe
// when active ambient sessions are confined to a single project: it then returns
// that project's most recently updated cc/* session. When they span multiple
// projects -- concurrent agents in different repos, the cross-agent-bleed case --
// it returns ambiguous=true and ok=false so durable creates force an explicit
// project= rather than guessing. It is only consulted when there is no binding.
func (s *Server) ambientFallback(ctx context.Context) (sess core.Session, ok bool, ambiguous bool) {
	projects, err := store.ActiveAmbientProjects(ctx, s.cfg.DB, ambientFallbackWindow)
	if err != nil {
		s.logger.Warn("mcp: ambient fallback projects", "error", err)
		return core.Session{}, false, false
	}
	if len(projects) != 1 {
		// Zero (nothing to inherit) or several (ambiguous) both decline; only the
		// multi-project case is a true ambiguity the caller must resolve.
		return core.Session{}, false, len(projects) > 1
	}
	sess, ok, err = store.LatestActiveAmbientSessionForProject(ctx, s.cfg.DB, projects[0], ambientFallbackWindow)
	if err != nil {
		s.logger.Warn("mcp: ambient fallback lookup", "error", err)
		return core.Session{}, false, false
	}
	return sess, ok, false
}

// record appends an event best-effort; a logging failure never fails a tool.
func (s *Server) record(ctx context.Context, kind core.EventKind, sessionID, project, itemID string, payload map[string]any) {
	if s.cfg.Events == nil {
		return
	}
	if _, err := s.cfg.Events.Record(ctx, core.Event{
		Kind: kind, SessionID: sessionID, ProjectSlug: project, ItemID: itemID, Payload: payload,
	}); err != nil {
		s.logger.Warn("mcp: record event", "kind", kind, "error", err)
	}
}
