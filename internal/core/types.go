package core

import (
	"slices"
	"time"
)

// ---------------------------------------------------------------------------
// Project
// ---------------------------------------------------------------------------

// Project groups memories, notes, sessions, tasks, and trials under a slug.
//
// ParentSlug, when set, points a child project (e.g. arctop-ios) at a shared
// parent (e.g. arctop-mobile-apps) whose active memories are injected into the
// child's briefing -- how a split keeps cross-platform knowledge shared without
// duplicating it. RetiredAt marks a project emptied by a split: kept for
// provenance and still readable, but flagged as no longer the live home.
type Project struct {
	ID          string     `json:"id"`
	Slug        string     `json:"slug"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	ParentSlug  string     `json:"parentSlug,omitempty"`
	RetiredAt   *time.Time `json:"retiredAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// Retired reports whether the project has been retired (emptied by a split).
func (p Project) Retired() bool { return p.RetiredAt != nil }

// ---------------------------------------------------------------------------
// Memory
// ---------------------------------------------------------------------------

// MemoryKind classifies a memory. It is the frontmatter `kind` field and is
// pinned/filtered differently per kind during briefing assembly.
type MemoryKind string

const (
	KindConstraint MemoryKind = "constraint" // hard rule that must hold
	KindRunbook    MemoryKind = "runbook"    // procedure to follow
	KindProtocol   MemoryKind = "protocol"   // interaction/coordination contract
	KindGotcha     MemoryKind = "gotcha"     // surprising pitfall
	KindDecision   MemoryKind = "decision"   // a choice and its rationale
	KindRefuted    MemoryKind = "refuted"    // a claim investigated and found false
	KindReference  MemoryKind = "reference"  // a durable pointer/fact
	KindStage      MemoryKind = "stage"      // a gated stage with status + gate lines
)

// MemoryKinds lists every valid kind, in briefing-priority-ish order.
var MemoryKinds = []MemoryKind{
	KindConstraint, KindRunbook, KindProtocol, KindGotcha,
	KindDecision, KindRefuted, KindReference, KindStage,
}

// Valid reports whether k is a recognized memory kind.
func (k MemoryKind) Valid() bool { return slices.Contains(MemoryKinds, k) }

// Memory is a single durable knowledge item, stored one-per-file with YAML
// frontmatter; this struct mirrors that frontmatter plus the body.
type Memory struct {
	ID            string     `json:"id"`
	Kind          MemoryKind `json:"kind"`
	Name          string     `json:"name"`
	Description   string     `json:"description"` // <=150 chars; the only text shown in indexes
	Project       string     `json:"project"`     // empty = global
	Body          string     `json:"body"`
	FilePath      string     `json:"filePath"`
	Tags          []string   `json:"tags"`
	Created       time.Time  `json:"created"`
	Updated       time.Time  `json:"updated"`
	ValidFrom     time.Time  `json:"validFrom"`
	InvalidAt     *time.Time `json:"invalidAt"`     // nil = still valid
	SupersededBy  string     `json:"supersededBy"`  // ULID of the replacement, "" = none
	SourceSession string     `json:"sourceSession"` // provenance
	ContentHash   string     `json:"contentHash"`
	// Extra preserves unknown frontmatter keys (e.g. Obsidian plugin fields) so
	// a parse -> render round-trip is lossless. Not mirrored to the index.
	Extra map[string]any `json:"extra,omitempty"`
}

// Active reports whether the memory is still valid (not superseded or archived).
// Inactive memories leave the briefing/prompt/recall indexes but remain readable.
func (m Memory) Active() bool { return m.InvalidAt == nil }

// ---------------------------------------------------------------------------
// Note
// ---------------------------------------------------------------------------

// Note is a work artifact (research finding, decision record, meeting summary),
// stored one-per-file with YAML frontmatter; this struct mirrors that
// frontmatter plus the body. Unlike a Memory it has no lifecycle/validity.
type Note struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`
	Project     string    `json:"project"` // empty = inbox
	Body        string    `json:"body"`
	FilePath    string    `json:"filePath"`
	Tags        []string  `json:"tags"`
	SourceURL   string    `json:"sourceUrl"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	ContentHash string    `json:"contentHash"`
	// Extra preserves unknown frontmatter keys for a lossless round-trip.
	Extra map[string]any `json:"extra,omitempty"`
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

// SessionStatus is the lifecycle state of an agent session.
type SessionStatus string

const (
	SessionActive    SessionStatus = "active"
	SessionCompleted SessionStatus = "completed"
	// SessionExpired is a session the reaper closed after it went idle past
	// SessionIdleTTL without a graceful session_end (a crashed/killed agent, or an
	// explicit session_start whose agent never called session_end). Distinct from
	// completed so the console can tell a harvested session from an abandoned one.
	SessionExpired SessionStatus = "expired"
)

// SessionIdleTTL is the no-activity age beyond which an active session is
// considered dead: heartbeats (MCP tool calls for bound sessions, the ambient
// hooks for cc/* sessions) bump updated_at, and anything quiet past this is
// reaped to SessionExpired and shown as idle in the console. It is the canonical
// liveness threshold shared by the gardener reaper and the console; it must
// comfortably exceed a long single agent turn so live work is never reaped.
const SessionIdleTTL = 45 * time.Minute

// LiveAsOf reports whether an active session counts as live at now: still active
// and updated within SessionIdleTTL. Completed/expired sessions are never live.
func (s Session) LiveAsOf(now time.Time) bool {
	return s.Status == SessionActive && now.Sub(s.UpdatedAt) < SessionIdleTTL
}

// Session is one agent work session. Ambient sessions are created by the
// SessionStart hook (named cc/{prefix}); explicit ones by session_start.
type Session struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	ProjectSlug     string         `json:"projectSlug"`
	Status          SessionStatus  `json:"status"`
	Findings        string         `json:"findings"`
	ClaudeSessionID string         `json:"claudeSessionId"`
	CWD             string         `json:"cwd"`
	Source          string         `json:"source"` // startup|resume|compact|clear|explicit
	Ambient         bool           `json:"ambient"`
	Metadata        map[string]any `json:"metadata"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

// ---------------------------------------------------------------------------
// Task
// ---------------------------------------------------------------------------

// TaskStatus is the state of a task in the ready-queue.
type TaskStatus string

const (
	TaskOpen       TaskStatus = "open"
	TaskInProgress TaskStatus = "in_progress"
	TaskDone       TaskStatus = "done"
	TaskDropped    TaskStatus = "dropped"
)

// TaskStatuses lists every valid task status.
var TaskStatuses = []TaskStatus{TaskOpen, TaskInProgress, TaskDone, TaskDropped}

// Valid reports whether s is a recognized task status.
func (s TaskStatus) Valid() bool { return slices.Contains(TaskStatuses, s) }

// Closed reports whether the status is terminal (done or dropped).
func (s TaskStatus) Closed() bool { return s == TaskDone || s == TaskDropped }

// Task is a unit of work with optional dependency edges. It is "ready" when all
// of its dependencies are closed.
//
// PlanSlug composes a task into a plan (see the plan:<slug> convention): a
// non-empty PlanSlug marks the task as a plan step, excluded from the default
// ready-queue but surfaced under a plan filter. ClaimedBy holds the session ULID
// that currently owns the task (empty when unclaimed); LeaseExpiresAt is when
// that claim lapses, after which the task is claimable again (lazy expiry, no
// sweeper).
type Task struct {
	ID             string     `json:"id"`
	ProjectSlug    string     `json:"projectSlug"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	Status         TaskStatus `json:"status"`
	CreatedBy      string     `json:"createdBy"`
	PlanSlug       string     `json:"planSlug,omitempty"`
	ClaimedBy      string     `json:"claimedBy,omitempty"`
	LeaseExpiresAt *time.Time `json:"leaseExpiresAt,omitempty"`
	DependsOn      []string   `json:"dependsOn,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	ClosedAt       *time.Time `json:"closedAt,omitempty"`
}

// ClaimLive reports whether the task is actively claimed as of now: in_progress,
// a non-empty holder, and an unexpired lease. An expired lease is not live (the
// task is reclaimable), matching ClaimTask's lazy expiry.
func (t Task) ClaimLive(now time.Time) bool {
	return t.ClaimedBy != "" && t.Status == TaskInProgress &&
		t.LeaseExpiresAt != nil && t.LeaseExpiresAt.After(now)
}

// ---------------------------------------------------------------------------
// Trial (research lab)
// ---------------------------------------------------------------------------

// TrialOutcome summarizes a trial result. Free-form by design (the store does
// not constrain it); these are the conventional values.
type TrialOutcome string

const (
	OutcomePass         TrialOutcome = "pass"
	OutcomeFail         TrialOutcome = "fail"
	OutcomePartial      TrialOutcome = "partial"
	OutcomeInconclusive TrialOutcome = "inconclusive"
)

// Trial records one expected-vs-actual experiment inside a lab, with optional
// structured metrics for native querying.
type Trial struct {
	ID          string         `json:"id"`
	Lab         string         `json:"lab"`
	Title       string         `json:"title"`
	Changes     string         `json:"changes"`
	Expected    string         `json:"expected"`
	Actual      string         `json:"actual"`
	Outcome     TrialOutcome   `json:"outcome"`
	Metrics     map[string]any `json:"metrics"`
	SessionID   string         `json:"sessionId"`
	ProjectSlug string         `json:"projectSlug"`
	CreatedAt   time.Time      `json:"createdAt"`
}

// ---------------------------------------------------------------------------
// Event (append-only log)
// ---------------------------------------------------------------------------

// EventKind identifies a kind of logged event. The append-only event log is the
// source for telemetry, retrieval stats, and the console feed.
type EventKind string

const (
	EventSessionStarted   EventKind = "session.started"
	EventSessionEnded     EventKind = "session.ended"
	EventMemoryWritten    EventKind = "memory.written"
	EventMemoryRead       EventKind = "memory.read"
	EventMemorySuperseded EventKind = "memory.superseded"
	EventMemoryArchived   EventKind = "memory.archived"
	EventMemoryMoved      EventKind = "memory.moved" // relocated to another project (reproject/split)
	EventNoteWritten      EventKind = "note.written"
	EventTrialRecorded    EventKind = "trial.recorded"
	EventTaskTransition   EventKind = "task.transition"
	EventInjected         EventKind = "retrieval.injected"
	EventGardenerAction   EventKind = "gardener.action"
	EventToolCall         EventKind = "tool.call"   // MCP tool invocation, logged live by the middleware (also the shape used by import)
	EventHookPrompt       EventKind = "hook.prompt" // a UserPromptSubmit that matched no memory (recall miss)

	// Claude Code plan-mode capture (PostToolUse/PermissionRequest/SubagentStop hooks).
	EventPlanCaptured     EventKind = "plan.captured"     // a plan-file iteration landed as a cc-plan note
	EventPlanPresented    EventKind = "plan.presented"    // the user was prompted to review the plan
	EventPlanApproved     EventKind = "plan.approved"     // the user approved the plan (ExitPlanMode)
	EventSubagentCaptured EventKind = "subagent.captured" // a planning subagent's prompt+report landed as a cc-agent note
)

// Event is one entry in the append-only log. Payload carries kind-specific
// detail (e.g. which memory names were injected).
type Event struct {
	ID          string         `json:"id"`
	TS          time.Time      `json:"ts"`
	Kind        EventKind      `json:"kind"`
	SessionID   string         `json:"sessionId,omitempty"`
	ProjectSlug string         `json:"projectSlug,omitempty"`
	ItemID      string         `json:"itemId,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
}
