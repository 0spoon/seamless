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

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/llm"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

const (
	serverName    = "Seamless"
	serverVersion = "0.0.0-dev"

	// ToolCount is the number of MCP tools registered so far. doctor asserts
	// against it; it grows to 26 by P4. P2 minimal loop = 15; P3 adds tasks (4)
	// and trials (3) = 22.
	ToolCount = 22

	// maxFindingsRunes caps session_end findings, matching the memory budget.
	maxFindingsRunes = 1500
)

// errNoSession is returned when a session-scoped tool is called with neither a
// bound session nor an explicit one.
var errNoSession = errors.New("no active session: call session_start first, or pass a session name")

// Config wires the MCP server's dependencies.
type Config struct {
	DB       *sql.DB
	Files    *files.Manager
	Retrieve *retrieve.Service
	Events   *events.Recorder
	Embedder llm.Embedder // may be nil (memory_write dedup hint is then skipped)
	APIKey   string
	Logger   *slog.Logger
}

// Server hosts the MCP tools and their per-connection session bindings.
type Server struct {
	mcp    *mcpserver.MCPServer
	cfg    Config
	logger *slog.Logger

	mu       sync.Mutex
	bindings map[string]binding // mcp client-session id -> binding
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
	s := &Server{
		cfg:      cfg,
		logger:   logger,
		bindings: make(map[string]binding),
	}
	s.mcp = mcpserver.NewMCPServer(
		serverName, serverVersion,
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
	s.mcp.AddTool(sessionStartTool(), s.handleSessionStart)
	s.mcp.AddTool(sessionUpdateTool(), s.handleSessionUpdate)
	s.mcp.AddTool(sessionEndTool(), s.handleSessionEnd)

	s.mcp.AddTool(memoryWriteTool(), s.handleMemoryWrite)
	s.mcp.AddTool(memoryAppendTool(), s.handleMemoryAppend)
	s.mcp.AddTool(memoryReadTool(), s.handleMemoryRead)
	s.mcp.AddTool(memoryDeleteTool(), s.handleMemoryDelete)

	s.mcp.AddTool(recallTool(), s.handleRecall)

	s.mcp.AddTool(notesCreateTool(), s.handleNotesCreate)
	s.mcp.AddTool(notesReadTool(), s.handleNotesRead)
	s.mcp.AddTool(notesUpdateTool(), s.handleNotesUpdate)
	s.mcp.AddTool(notesAppendTool(), s.handleNotesAppend)
	s.mcp.AddTool(notesDeleteTool(), s.handleNotesDelete)

	s.mcp.AddTool(projectListTool(), s.handleProjectList)
	s.mcp.AddTool(projectCreateTool(), s.handleProjectCreate)

	s.mcp.AddTool(tasksAddTool(), s.handleTasksAdd)
	s.mcp.AddTool(tasksUpdateTool(), s.handleTasksUpdate)
	s.mcp.AddTool(tasksReadyTool(), s.handleTasksReady)
	s.mcp.AddTool(tasksListTool(), s.handleTasksList)

	s.mcp.AddTool(labOpenTool(), s.handleLabOpen)
	s.mcp.AddTool(trialRecordTool(), s.handleTrialRecord)
	s.mcp.AddTool(trialQueryTool(), s.handleTrialQuery)
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

// resolveProject prefers an explicit project argument, then the bound session's
// project, then the most recent ambient session's project (write-scope
// fallback), then the global scope.
func (s *Server) resolveProject(ctx context.Context, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if b, ok := s.getBinding(ctx); ok {
		return b.project
	}
	if sess, ok := s.ambientFallback(ctx); ok {
		return sess.ProjectSlug
	}
	return ""
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
