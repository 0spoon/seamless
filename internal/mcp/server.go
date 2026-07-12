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

// errAmbiguousScope is returned by resolveWriteScope when a durable create has no
// explicit project, no bound session, and no ambient session to inherit from --
// so the write would silently land in the global scope. The agent must choose.
var errAmbiguousScope = errors.New(
	"ambiguous scope: no bound or ambient session to infer the project from; " +
		"pass project=<slug> for a project item, or project=global for a global one")

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

// resolveProject prefers an explicit project argument (with the global token
// normalized), then the bound session's project, then the most recent ambient
// session's project (write-scope fallback), then the global scope. It never
// errors: reads and searches quietly fall back to global. Durable creates use
// resolveWriteScope instead, which rejects that ambiguity.
func (s *Server) resolveProject(ctx context.Context, explicit string) string {
	if explicit != "" {
		return normalizeProject(explicit)
	}
	if b, ok := s.getBinding(ctx); ok {
		return b.project
	}
	if sess, ok := s.ambientFallback(ctx); ok {
		return sess.ProjectSlug
	}
	return ""
}

// resolveWriteScope is resolveProject for durable creates (memory/note/task): it
// returns the same project, but errors with errAmbiguousScope instead of
// silently defaulting to global when nothing pins the scope. An explicit project
// -- including project=global -- is always unambiguous, as is any bound or
// ambient session (even a deliberately global one).
func (s *Server) resolveWriteScope(ctx context.Context, explicit string) (string, error) {
	if explicit != "" {
		return normalizeProject(explicit), nil
	}
	if b, ok := s.getBinding(ctx); ok {
		return b.project, nil
	}
	if sess, ok := s.ambientFallback(ctx); ok {
		return sess.ProjectSlug, nil
	}
	return "", errAmbiguousScope
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

// boundSession returns the bound session's ULID, the most recent ambient
// session's ULID (write-scope fallback), or "".
func (s *Server) boundSession(ctx context.Context) string {
	if b, ok := s.getBinding(ctx); ok {
		return b.sessionID
	}
	if sess, ok := s.ambientFallback(ctx); ok {
		return sess.ID
	}
	return ""
}

// ambientFallback returns the ambient session an unbound connection's writes
// should attribute to: the most recently updated active cc/* session within the
// fallback window. It is only consulted when there is no explicit binding.
func (s *Server) ambientFallback(ctx context.Context) (core.Session, bool) {
	sess, ok, err := store.LatestActiveAmbientSession(ctx, s.cfg.DB, ambientFallbackWindow)
	if err != nil {
		s.logger.Warn("mcp: ambient fallback lookup", "error", err)
		return core.Session{}, false
	}
	return sess, ok
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
