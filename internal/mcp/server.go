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
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/0spoon/seamless/internal/agentguide"
	"github.com/0spoon/seamless/internal/capture"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/gardener"
	"github.com/0spoon/seamless/internal/llm"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
	"github.com/0spoon/seamless/internal/validate"
)

const (
	serverName    = "Seamless"
	serverVersion = "0.0.0-dev"

	// ToolCount is the number of MCP tools registered. doctor asserts the actual
	// registered count (Server.NumTools) equals it. P2 minimal loop = 15; P3 adds
	// tasks (4) + trials (3) = 22; P4 adds gardener (2) + capture_url +
	// usage_summary = 26; plans-as-composition adds tasks_claim + tasks_release = 28;
	// gardener_request = 29; gardener_split = 30; favorite_set = 31.
	ToolCount = 31

	// globalNamespace is the reserved project token an agent passes to
	// deliberately target the global (cross-project) scope, instead of relying on
	// an empty project (which is easy to hit by accident). "_global" -- the
	// on-disk directory name -- is accepted as a synonym.
	globalNamespace = "global"

	// allProjectsToken is the reserved project token that widens a read to every
	// project on the machine. Only gardener_request interprets it, and only
	// because scanning everything is a real reorganization workflow -- but one
	// that must be asked for. It exists so that capability could survive being
	// taken off the empty project, where it was the silent default.
	allProjectsToken = "all"
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
		"name the scope with project=<slug> or project=global")

// errAmbiguousScope is returned when a durable create has no explicit project and
// no bound session, and active ambient sessions span multiple projects
// (concurrent agents in different repos). Inheriting the machine-latest ambient
// here is exactly how a write bleeds into another agent's project, so the caller
// must disambiguate with project=.
// errAmbiguousSession is returned by resolveSession when a session_update/end
// gives no explicit session_id or session name, has no bound session, and more
// than one active ambient session -- across projects, or two agents' cc/* or cx/*
// ambients in the same repo -- could be the target. Guessing here would complete or
// overwrite another agent's session, so the caller must name it explicitly.
var errAmbiguousSession = errors.New(
	"ambiguous session: no bound session, and multiple active ambient sessions " +
		"could be the target; pass session_id=<ULID> or session=<name>")

var errAmbiguousScope = errors.New(
	"ambiguous scope: no bound session, and active ambient sessions span multiple " +
		"projects (concurrent agents); name the scope with project=<slug> or project=global")

// errAmbiguousActor is returned by resolveActor when an identity-sensitive task
// operation (a claim, a release, or a mutation of a task under a live claim) has
// no bound session and more than one active ambient session could be the caller
// -- concurrent agents in the same project. Guessing the actor here would record a
// claim under, or release/mutate a claim held by, the WRONG agent's session,
// locking the true holder out of its own task (the cross-agent claim-bleed bug);
// so the caller must name itself. Its cc/<id> or cx/<id> ambient name is printed
// in the session briefing.
var errAmbiguousActor = errors.New(
	"ambiguous agent: no bound session and multiple agents are active, so the acting " +
		"session cannot be inferred; name yourself with session=<your cc/... or cx/... from the briefing> or session_id=<ULID>")

// writeScopeHelp is the tail resolveWriteScope appends to either scope error. It
// carries the two facts an agent stuck at this decision cannot get anywhere else,
// and whose absence reliably produces the wrong answer:
//
// A new slug is a legal choice. resolveWriteScope registers whatever slug it is
// given, so "will an unmapped slug error?" -- the question with no cheap way to be
// answered at the call site -- is settled here as "no". An agent that cannot
// answer it treats naming a new project as the risky move.
//
// global is not the neutral fallback it looks like. It reads as the conservative
// pick (it claims nothing about which project owns the item) while being the one
// choice with machine-wide blast radius: a global memory is injected into EVERY
// project's briefing, indefinitely. The safe-looking option is the expensive one,
// so the error says so rather than leaving the two to look symmetric.
const writeScopeHelp = "an unknown slug CREATES that project, so a new project is always " +
	"an available choice and never an error; project=global is not a neutral fallback -- it puts " +
	"the item in EVERY project's briefing, so choose it only for genuinely cross-project knowledge"

// maxScopeErrorProjects caps the slug list a scope error carries, so a machine
// with many projects still returns an error an agent can actually read.
const maxScopeErrorProjects = 30

// writeProjectArgDesc documents the project argument on every tool that resolves
// scope through resolveWriteScope. It is one string rather than five hand-worded
// ones because the fact an agent needs BEFORE it calls -- that naming a new
// project is an ordinary working choice, and that global is not the free default
// it resembles -- is precisely the fact that would go stale in four of five copies.
// The scope error carries the same guidance; this is the half that prevents the
// bad call rather than correcting it.
const writeProjectArgDesc = "project slug; defaults to the bound/ambient session's project. " +
	"An unknown slug CREATES that project -- naming a new one is normal and never an error. " +
	"Pass project=global ONLY for knowledge that belongs in EVERY project's briefing; it is not a " +
	"neutral default. With no session and no explicit project the call is rejected as ambiguous."

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

// validateProjectArg normalizes an explicit project argument and rejects slugs
// unsafe as a path segment (separators, "..", null bytes). The project becomes a
// directory under the memory/ and notes/ trees, and validate.PathWithinDir only
// guards the data-dir boundary: an unvalidated slug like "../notes/_global" cleans
// to a path INSIDE the data dir but outside its tree, letting one item clobber
// another tree's files. Every tool taking a project/slug argument resolves it
// through here (via resolveReadScope/resolveWriteScope or directly).
//
// It also rejects the widening token, which project_create has always refused for
// the same reason: gardener_request reads "all" as every project, so a project
// slugged "all" is permanently unaddressable through it. Shape validation alone
// used to let the token through here as an ordinary slug -- harmless only while
// nothing acted on it, and no longer, now that resolveWriteScope registers the
// slug it is handed. The two ways to name a project scope agree on what a project
// may be called.
func validateProjectArg(explicit string) (string, error) {
	project := normalizeProject(explicit)
	if project == "" {
		return "", nil
	}
	if project == allProjectsToken {
		return "", fmt.Errorf("project %q is reserved: gardener_request reads it as every project; name a real slug", explicit)
	}
	if err := validate.Name(project); err != nil {
		return "", fmt.Errorf("invalid project %q: %w", explicit, err)
	}
	return project, nil
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
	// ToolEventMaxChars caps each captured field (tool.call args value/result,
	// session findings) of a logged Interactions event at this many runes. 0 =
	// unlimited (the default): content is logged in full.
	ToolEventMaxChars int
	// CaptureAllowedPorts are the destination ports capture_url may dial
	// (config.Capture.AllowedPorts). Empty means the capture package's 80/443
	// default, never "any port".
	CaptureAllowedPorts []int
	Logger              *slog.Logger
}

// Server hosts the MCP tools and their per-connection session bindings.
type Server struct {
	mcp       *mcpserver.MCPServer
	cfg       Config
	logger    *slog.Logger
	fetcher   *capture.URLFetcher // SSRF-safe URL fetch backing capture_url
	toolNames []string            // registered tool names, in registration order

	// toolSchemas is validateMiddleware's lookup: tool name -> declared input
	// schema. Written only by addTool during New (before the server serves) and
	// read-only thereafter, so it needs no lock.
	toolSchemas map[string]mcp.ToolInputSchema

	mu        sync.Mutex
	bindings  map[string]binding // mcp client-session id -> binding
	lastSweep time.Time          // last opportunistic bindings sweep (guarded by mu)
}

// NumTools returns the number of registered MCP tools. doctor asserts it equals
// ToolCount, catching a tool that was written but never wired into registerTools.
func (s *Server) NumTools() int { return len(s.toolNames) }

// addTool registers one tool and records its name (so NumTools stays accurate)
// and its input schema (so validateMiddleware can enforce it).
//
// Recording the schema HERE, in the same statement that creates the tool, is what
// makes validation impossible to forget: a tool cannot exist without its schema
// being recorded, because there is no other way to register one.
func (s *Server) addTool(t mcp.Tool, h mcpserver.ToolHandlerFunc) {
	s.toolNames = append(s.toolNames, t.Name)
	s.toolSchemas[t.Name] = t.InputSchema
	s.mcp.AddTool(t, h)
}

// toolSchema returns a registered tool's declared input schema.
func (s *Server) toolSchema(name string) (mcp.ToolInputSchema, bool) {
	schema, ok := s.toolSchemas[name]
	return schema, ok
}

// binding is the session context inherited by later tool calls on a connection.
type binding struct {
	sessionID string    // ULID of the bound session; "" marks a session-less (lab-only) entry
	project   string    // resolved project slug ("" = global)
	lab       string    // current research lab (set by lab_open), inherited by trial_record
	touchedAt time.Time // last set/lookup; the sweep's staleness signal for session-less bindings
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
		cfg:         cfg,
		logger:      logger,
		fetcher:     capture.NewURLFetcher(cfg.CaptureAllowedPorts),
		bindings:    make(map[string]binding),
		toolSchemas: make(map[string]mcp.ToolInputSchema),
	}
	s.mcp = mcpserver.NewMCPServer(
		serverName, version,
		mcpserver.WithInstructions(agentguide.MCPInstructions),
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithRecovery(),
		// mcp-go applies middlewares in reverse registration order, so the runtime
		// nesting is auth(log(validate(handler))):
		//
		//   - auth outermost: an unauthorized caller reaches neither the logger nor
		//     the validator, so a bad key never earns schema feedback.
		//   - validate inside log: a rejected call IS recorded. A silent rejection
		//     is the same observability hole as a silent coercion -- the operator
		//     would see "the agent stopped calling tasks_add" rather than "the agent
		//     is misspelling depends_on".
		mcpserver.WithToolHandlerMiddleware(s.authMiddleware),
		mcpserver.WithToolHandlerMiddleware(s.logMiddleware),
		mcpserver.WithToolHandlerMiddleware(s.validateMiddleware),
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
	s.addTool(tasksClaimTool(), s.handleTasksClaim)
	s.addTool(tasksReleaseTool(), s.handleTasksRelease)

	s.addTool(labOpenTool(), s.handleLabOpen)
	s.addTool(trialRecordTool(), s.handleTrialRecord)
	s.addTool(trialQueryTool(), s.handleTrialQuery)

	s.addTool(gardenerProposalsTool(), s.handleGardenerProposals)
	s.addTool(gardenerRequestTool(), s.handleGardenerRequest)
	s.addTool(gardenerSplitTool(), s.handleGardenerSplit)
	s.addTool(gardenerApplyTool(), s.handleGardenerApply)

	s.addTool(captureURLTool(), s.handleCaptureURL)
	s.addTool(usageSummaryTool(), s.handleUsageSummary)

	s.addTool(favoriteSetTool(), s.handleFavoriteSet)
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
	s.bindings[id] = binding{sessionID: sessionID, project: project, touchedAt: time.Now()}
	s.mu.Unlock()
	s.maybeSweepBindings(ctx)
}

// getBinding returns the connection's session binding. A session-less entry --
// created by setBindingLab on a connection that never ran session_start -- is
// not one: reporting it as bound would hand resolveWriteScope/resolveReadScope
// an empty project (the global scope) and shadow the ambient fallback, silently
// globalizing unscoped writes after a bare lab_open. Such entries are visible
// only through rawBinding.
func (s *Server) getBinding(ctx context.Context) (binding, bool) {
	b, ok := s.rawBinding(ctx)
	return b, ok && b.sessionID != ""
}

// rawBinding returns the connection's bindings entry whether or not it carries
// a session, refreshing its touchedAt. Only lab affinity reads through it --
// a lab is legitimately bound on a connection with no session.
func (s *Server) rawBinding(ctx context.Context) (binding, bool) {
	id := s.mcpSessionID(ctx)
	if id == "" {
		return binding{}, false
	}
	s.mu.Lock()
	b, ok := s.bindings[id]
	if ok {
		b.touchedAt = time.Now()
		s.bindings[id] = b
	}
	s.mu.Unlock()
	s.maybeSweepBindings(ctx)
	return b, ok
}

// evictSessionBindings drops every connection binding pointing at sessionID.
// session_end calls it the moment a session completes, so a long-lived daemon
// does not keep a binding for a session that is over; sessions ended by other
// paths (the SessionEnd hook, the idle reaper) are caught by the sweep instead.
func (s *Server) evictSessionBindings(sessionID string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	for id, b := range s.bindings {
		if b.sessionID == sessionID {
			delete(s.bindings, id)
		}
	}
	s.mu.Unlock()
}

// bindingSweepInterval gates the opportunistic bindings sweep: at most one tool
// call per interval pays for the single SQLite lookup the sweep costs.
const bindingSweepInterval = 5 * time.Minute

// bindingIdleTTL is the sweep's fallback for session-less bindings (a lab_open
// with no session_start): with no session whose lifecycle can end them, they are
// evicted after going untouched this long. It mirrors ambientFallbackWindow --
// past that, the connection would no longer inherit anything useful anyway.
const bindingIdleTTL = ambientFallbackWindow

// maybeSweepBindings opportunistically evicts stale connection bindings, at most
// once per bindingSweepInterval. A binding is stale when its session is no longer
// active (completed via session_end or the SessionEnd hook, or expired by the
// idle reaper -- the transport never tells us a client went away) or, for a
// session-less binding, untouched past bindingIdleTTL. Without eviction the map
// grows by one entry per MCP connection for the daemon's lifetime. The DB read
// runs outside the lock; the delete pass re-checks each entry under the lock so
// a binding replaced or refreshed mid-sweep survives.
func (s *Server) maybeSweepBindings(ctx context.Context) {
	if s.cfg.DB == nil {
		return
	}
	now := time.Now()
	type sample struct{ id, sessionID string }
	s.mu.Lock()
	if now.Sub(s.lastSweep) < bindingSweepInterval || len(s.bindings) == 0 {
		s.mu.Unlock()
		return
	}
	s.lastSweep = now
	samples := make([]sample, 0, len(s.bindings))
	sessionIDs := make([]string, 0, len(s.bindings))
	for id, b := range s.bindings {
		samples = append(samples, sample{id: id, sessionID: b.sessionID})
		if b.sessionID != "" {
			sessionIDs = append(sessionIDs, b.sessionID)
		}
	}
	s.mu.Unlock()

	active, err := store.ActiveSessionIDs(ctx, s.cfg.DB, sessionIDs)
	if err != nil {
		s.logger.Warn("mcp: binding sweep", "error", err)
		return
	}

	s.mu.Lock()
	for _, smp := range samples {
		b, ok := s.bindings[smp.id]
		if !ok || b.sessionID != smp.sessionID {
			continue // re-bound mid-sweep; the fresh binding is not ours to judge
		}
		stale := (smp.sessionID != "" && !active[smp.sessionID]) ||
			(smp.sessionID == "" && now.Sub(b.touchedAt) >= bindingIdleTTL)
		if stale {
			delete(s.bindings, smp.id)
		}
	}
	s.mu.Unlock()
}

// ambientFallbackWindow bounds how stale an ambient (cc/* or cx/*) session may be and
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
		return validateProjectArg(explicit)
	}
	if b, ok := s.getBinding(ctx); ok {
		return b.project, nil
	}
	sess, ok, ambiguous := s.ambientFallback(ctx)
	if ok {
		return sess.ProjectSlug, nil
	}
	if ambiguous {
		return "", s.scopeErr(ctx, errAmbiguousScope, "")
	}
	return "", nil
}

// scopeErr decorates a scope sentinel with the guidance an agent needs to answer
// it: the caller's path-specific help (write only -- a read creates nothing, so
// telling a reader that a new slug would be created is advice for a different
// question), plus the slugs that actually exist. It wraps rather than replaces,
// so errors.Is still sees the sentinel.
func (s *Server) scopeErr(ctx context.Context, base error, help string) error {
	tail := ""
	if help != "" {
		tail += "; " + help
	}
	if known := s.knownProjects(ctx); known != "" {
		tail += "; known projects: " + known
	}
	if tail == "" {
		return base
	}
	return fmt.Errorf("%w%s", base, tail)
}

// knownProjects renders the active project slugs for a scope error, so an agent
// picking a project can match one that exists instead of coining a near-duplicate
// of it -- the failure mode registering unknown slugs would otherwise invite.
// Retired projects (emptied by a split) are real slugs but not ones to write into,
// so they are left out. Best-effort: on a store failure the error simply carries
// no list, rather than trading a scope error the agent can act on for a database
// error it cannot.
func (s *Server) knownProjects(ctx context.Context) string {
	ps, err := store.ListProjects(ctx, s.cfg.DB)
	if err != nil {
		s.logger.Warn("mcp: scope error project list", "error", err)
		return ""
	}
	slugs := make([]string, 0, len(ps))
	for _, p := range ps {
		if p.RetiredAt != nil {
			continue
		}
		slugs = append(slugs, p.Slug)
	}
	if len(slugs) == 0 {
		return ""
	}
	if len(slugs) > maxScopeErrorProjects {
		extra := len(slugs) - maxScopeErrorProjects
		slugs = append(slugs[:maxScopeErrorProjects:maxScopeErrorProjects],
			fmt.Sprintf("(+%d more -- project_list)", extra))
	}
	return strings.Join(slugs, ", ")
}

// resolveWriteScope is resolveReadScope for durable creates (memory/note/task/
// captured-note/trial): it returns the same project, but errors with errNoScope
// instead of defaulting to global when nothing pins the scope. An explicit project
// -- including project=global -- is always unambiguous, as is any bound session
// or a single-project ambient fallback. When active ambient sessions span
// multiple projects (concurrent agents), it refuses to guess rather than bleed
// the write into the wrong project.
//
// A named slug is also registered, so a durable create into a not-yet-known
// project produces a first-class project instead of an orphan scope. The file and
// its index row land under the slug either way; what was missing was the
// projects-table row, leaving the project invisible to project_list and the
// console until some unrelated path (a session_start in a mapped repo,
// map-repo, an import) happened to backfill it. The gap was silent and
// self-fulfilling: an agent had no way to tell a new-project write from a broken
// one, so it avoided naming a new project and reached for project=global -- the
// one scope that bleeds into every project's briefing. Registering here is what
// lets writeScopeHelp promise that a new slug works.
//
// It registers a typo'd slug too. That is not a regression but the same orphan
// made visible: the typo already created a scope on disk, and a project_list the
// console renders is where a wrong one can be noticed and merged, rather than
// lingering as a directory nothing lists. EnsureProject is idempotent, and a
// no-op for the global scope.
func (s *Server) resolveWriteScope(ctx context.Context, explicit string) (string, error) {
	if explicit != "" {
		project, err := validateProjectArg(explicit)
		if err != nil {
			return "", err
		}
		if _, err := store.EnsureProject(ctx, s.cfg.DB, project, project); err != nil {
			return "", err
		}
		return project, nil
	}
	if b, ok := s.getBinding(ctx); ok {
		return b.project, nil
	}
	sess, ok, ambiguous := s.ambientFallback(ctx)
	if ok {
		return sess.ProjectSlug, nil
	}
	if ambiguous {
		return "", s.scopeErr(ctx, errAmbiguousScope, writeScopeHelp)
	}
	return "", s.scopeErr(ctx, errNoScope, writeScopeHelp)
}

// setBindingLab records the current research lab on the connection's binding, so
// a later trial_record can inherit it. It is a no-op on a stateless transport
// (no client session id) -- such callers pass lab explicitly. On a connection
// with no session binding it deliberately creates a session-less entry: that
// preserves lab affinity, and getBinding refuses to report the entry as a
// session binding, so it cannot leak an empty project into scope resolution.
func (s *Server) setBindingLab(ctx context.Context, lab string) {
	id := s.mcpSessionID(ctx)
	if id == "" {
		return
	}
	s.mu.Lock()
	b := s.bindings[id]
	b.lab = lab
	b.touchedAt = time.Now()
	s.bindings[id] = b
	s.mu.Unlock()
}

// boundLab returns the connection's current lab, or "". It reads the raw
// entry, not the session binding: lab affinity survives on a connection that
// never ran session_start.
func (s *Server) boundLab(ctx context.Context) string {
	if b, ok := s.rawBinding(ctx); ok {
		return b.lab
	}
	return ""
}

// boundSession returns the bound session's ULID, a single unambiguous ambient
// session's ULID (write-scope fallback), or "". It backs provenance/telemetry
// stamping (memory SourceSession, event SessionID, created_by), where collapsing a
// project's concurrent ambients to the most recent is an acceptable best-effort
// attribution. Identity-sensitive task ownership (claim/release/update-under-lock)
// must NOT use it -- see resolveActor, which refuses to guess.
func (s *Server) boundSession(ctx context.Context) string {
	if b, ok := s.getBinding(ctx); ok {
		return b.sessionID
	}
	if sess, ok, _ := s.ambientFallback(ctx); ok {
		return sess.ID
	}
	return ""
}

// boundSessionModel returns the model id recorded on the session boundSession
// resolves to, or "". It stamps knowledge attribution (Memory.Model,
// Note.Model) at write time -- read from the session row on every write, not
// cached in the binding, because the hooks update the session's model in place
// when the agent switches models mid-session. Best-effort like boundSession:
// no session, no recorded model, or a lookup failure all yield "" (attribution
// must never block a write); the failure is logged rather than silently eaten.
func (s *Server) boundSessionModel(ctx context.Context) string {
	id := s.boundSession(ctx)
	if id == "" {
		return ""
	}
	sess, ok, err := store.SessionByID(ctx, s.cfg.DB, id)
	if err != nil {
		s.logger.Warn("model attribution: session lookup", "session_id", id, "error", err)
		return ""
	}
	if !ok {
		return ""
	}
	return sess.Model
}

// resolveActor resolves the session ULID that OWNS an identity-sensitive task
// operation -- a claim, a release, or a mutation of a task under a live claim. It
// is the strict counterpart of boundSession: where boundSession may collapse a
// project's concurrent ambients to the most recently updated one (fine for
// stamping provenance), an actor must never be GUESSED, because the guess becomes
// claimed_by -- or is compared against it -- and a wrong guess locks the true
// holder out of its own task (the cross-agent claim-bleed bug).
//
// Resolution mirrors resolveSession: an explicit session_id (ULID) or session name
// the agent passes to name itself, then the connection binding, then -- only for
// the solo-agent case -- the single active ambient. Concurrent same-project
// ambients are ambiguous (errAmbiguousActor): the agent disambiguates with the
// cc/<id> or cx/<id> its briefing prints. Because the sole-ambient case resolves the same
// ULID on every call, a solo agent's identity survives a lost binding (reconnect,
// daemon restart) with no re-session_start -- the binding is a cache here, not the
// source of truth.
//
// ok is false with a nil error only when there is no session at all to act as. An
// explicit session_id/session that does not resolve is a loud error, not a silent
// fall-through -- naming a bad identity should fail, not attribute elsewhere.
func (s *Server) resolveActor(ctx context.Context, req mcp.CallToolRequest) (string, bool, error) {
	if id := argString(req, "session_id"); id != "" {
		sess, ok, err := store.SessionByID(ctx, s.cfg.DB, id)
		if err != nil {
			return "", false, err
		}
		if !ok {
			return "", false, fmt.Errorf("session_id %q not found", id)
		}
		return sess.ID, true, nil
	}
	if name := argString(req, "session"); name != "" {
		sess, ok, err := store.SessionByName(ctx, s.cfg.DB, name)
		if err != nil {
			return "", false, err
		}
		if !ok {
			return "", false, fmt.Errorf("session %q not found", name)
		}
		return sess.ID, true, nil
	}
	if b, ok := s.getBinding(ctx); ok {
		return b.sessionID, true, nil
	}
	sess, ok, ambiguous, err := s.ambientSessionTarget(ctx)
	if err != nil {
		return "", false, err
	}
	if ambiguous {
		return "", false, errAmbiguousActor
	}
	return sess.ID, ok, nil
}

// ambientFallback returns the ambient session an unbound connection's writes
// should attribute to. Because such a connection carries no project signal (MCP
// tool calls receive no cwd, and no session_start ran), attribution is only safe
// when active ambient sessions are confined to a single project: it then returns
// that project's most recently updated cc/* or cx/* session. When they span multiple
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
