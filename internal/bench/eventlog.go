// Event-log checks: the "did the mechanism fire, and did the agent use it?"
// layer, read back from the run's preserved Seamless data dir.
//
// ANTI-BRITTLENESS RULE (plan:seambench, 2026-07-28 addendum item 5): never
// string-match briefing layout text. The briefing layout is the fastest-churning
// surface in the repo and is itself the regression surface this benchmark
// exists to guard, so a grader keyed to its wording would fail the moment the
// thing it measures improves. Everything here asserts on event KINDS, DB rows,
// and stable markers: memory names, tool names, hook names, project slugs.
//
// The scenario seeders write no events, so every event in a preserved log was
// produced by the run itself -- no seed/run disambiguation is needed.

package bench

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/store"
)

// eventReadCap bounds the log read for one run. An agent run produces events in
// the hundreds; saturating this means something pathological happened, and the
// grader says so rather than quietly grading a slice of the run.
const eventReadCap = 100000

// hookSessionStart is the payload marker the SessionStart hook stamps on its
// retrieval.injected event (internal/hooks writes the hook's CLI-arg form,
// "session-start"). Compared through normalizeHook so the event-name form
// ("SessionStart") matches too -- the marker is the hook's identity, not its
// spelling.
const hookSessionStart = "session-start"

// runLog is one run's preserved Seamless state: the whole event log plus the
// still-open DB behind it, so a check can assert on end state (a task's status)
// as readily as on the events that got it there.
type runLog struct {
	ctx     context.Context
	db      *sql.DB
	project string
	// Events are chronological, oldest first.
	Events []core.Event
	// Saturated reports that the log hit eventReadCap and the oldest events
	// were not read.
	Saturated bool
}

// openRunLog opens the preserved data dir's database and reads its event log.
// The caller must close it. A data dir with no database is a broken artifact,
// not an empty run: store.Open would happily create a fresh one and every check
// would then fail for the wrong reason.
func openRunLog(ctx context.Context, dataDir, project string) (*runLog, error) {
	dbPath := filepath.Join(dataDir, "seam.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("%w: data dir %s has no seam.db: %w", ErrMissingArtifacts, dataDir, err)
	}
	// store.Open migrates what it opens. That is safe here and deliberate: the
	// artifact is a COPY of the arm's data dir, so an older run directory can
	// still be graded by a newer build without touching anything live.
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("bench: open run data dir %s: %w", dataDir, err)
	}
	evs, err := events.NewRecorder(db).RecentExcluding(ctx, eventReadCap)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("bench: read run event log %s: %w", dataDir, err)
	}
	slices.Reverse(evs) // RecentExcluding is newest-first; checks read forward
	return &runLog{ctx: ctx, db: db, project: project, Events: evs, Saturated: len(evs) >= eventReadCap}, nil
}

func (l *runLog) close() error { return l.db.Close() }

// ofKind returns the log's events of one kind, in order.
func (l *runLog) ofKind(kind core.EventKind) []core.Event {
	var out []core.Event
	for _, e := range l.Events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// metrics derives the grader-owned half of Metrics from the event log. The
// runner owns the run-shape fields (turns, tokens, cost, duration) and this
// never touches them.
func (l *runLog) metrics() Metrics {
	m := Metrics{ToolCallsByName: map[string]int{}}
	var recallSurfaced int
	for _, e := range l.Events {
		switch e.Kind {
		case core.EventToolCall:
			m.ToolCalls++
			if tool := payloadString(e, "tool"); tool != "" {
				m.ToolCallsByName[tool]++
			}
			if payloadBool(e, "is_error") {
				m.ToolErrors++
			}
		case core.EventInjected:
			// A hook injection is context the agent was handed; the recall
			// tool records the same kind for what it surfaced, which is a tool
			// result and counted under Recalls instead.
			switch {
			case payloadString(e, "hook") != "":
				m.Injections++
			case strings.HasPrefix(payloadString(e, "source"), "recall"):
				recallSurfaced++
			}
		case core.EventMemoryRead:
			m.MemoryReads++
		case core.EventMemoryWritten:
			m.MemoryWrites++
		case core.EventRecallMiss:
			m.RecallMisses++
		case core.EventAgentMishap:
			m.Mishaps++
		case core.EventTaskTransition:
			m.TaskTransitions++
		case core.EventSessionEnded:
			if payloadString(e, "findings") != "" {
				m.SessionFindings++
			}
		}
	}
	m.Recalls = m.ToolCallsByName["recall"]
	if m.Recalls == 0 {
		// Fallback for a log whose transport-level tool.call rows were pruned
		// by the gardener's retention pass: every recall either surfaced hits
		// or recorded a miss.
		m.Recalls = recallSurfaced + m.RecallMisses
	}
	if len(m.ToolCallsByName) == 0 {
		m.ToolCallsByName = nil
	}
	return m
}

// ---------------------------------------------------------------------------
// reusable event checks
// ---------------------------------------------------------------------------

// briefingInjected asserts the SessionStart hook injection fired at all -- the
// mechanism arriving, said in the one way that does not depend on what the
// briefing said.
func briefingInjected() eventCheck {
	return eventCheck{name: "SessionStart briefing injected", gate: true, fn: func(l *runLog) (bool, string, error) {
		n, hooks := l.sessionStartInjections()
		if n > 0 {
			return true, fmt.Sprintf("%d session-start injection(s)", n), nil
		}
		if len(hooks) == 0 {
			return false, "no hook injection reached the agent", nil
		}
		return false, "no session-start injection; hooks that did fire: " + strings.Join(hooks, ", "), nil
	}}
}

// sessionStartInjections counts the SessionStart briefing injections in the
// log, and reports which other hooks fired for the evidence line.
func (l *runLog) sessionStartInjections() (n int, others []string) {
	byHook := map[string]int{}
	for _, e := range l.ofKind(core.EventInjected) {
		if hook := payloadString(e, "hook"); hook != "" {
			byHook[normalizeHook(hook)]++
		}
	}
	n = byHook[normalizeHook(hookSessionStart)]
	return n, sortedKeys(byHook)
}

// briefingInjectedTimes asserts the SessionStart injection fired at least n
// times -- the multi-step form of briefingInjected, one per agent session: a
// handoff run where the second session never got its briefing is a mechanism
// defect, not an agent choice.
func briefingInjectedTimes(n int) eventCheck {
	name := fmt.Sprintf("SessionStart briefing injected in all %d sessions", n)
	return eventCheck{name: name, gate: true, fn: func(l *runLog) (bool, string, error) {
		got, hooks := l.sessionStartInjections()
		if got >= n {
			return true, fmt.Sprintf("%d session-start injection(s)", got), nil
		}
		if got == 0 && len(hooks) == 0 {
			return false, "no hook injection reached any session", nil
		}
		return false, fmt.Sprintf("%d of %d session-start injection(s); hooks that did fire: %s",
			got, n, strings.Join(hooks, ", ")), nil
	}}
}

// handoffCarried asserts the write half of a handoff: some durable record --
// a session_end with findings, a memory write, or a note write -- existed
// BEFORE the last session's briefing was injected, so the later session had
// something to inherit. Observed, not a gate: whether the earlier agent
// recorded anything is exactly the behaviour under measurement.
func handoffCarried() eventCheck {
	return eventCheck{name: "handoff recorded before the next session", fn: func(l *runLog) (bool, string, error) {
		lastStart := -1
		for i, e := range l.Events {
			if e.Kind == core.EventInjected && normalizeHook(payloadString(e, "hook")) == normalizeHook(hookSessionStart) {
				lastStart = i
			}
		}
		if lastStart < 0 {
			return false, "no session-start injection to hand off to", nil
		}
		for _, e := range l.Events[:lastStart] {
			switch e.Kind {
			case core.EventMemoryWritten, core.EventNoteWritten:
				return true, string(e.Kind) + " before the last session started", nil
			case core.EventSessionEnded:
				if strings.TrimSpace(payloadString(e, "findings")) != "" {
					return true, "session_end findings before the last session started", nil
				}
			}
		}
		return false, "nothing durable was recorded before the last session started", nil
	}}
}

// trialsQueried asserts the agent consulted the research lab's recorded
// trials -- the dead-end record -- via the trial_query tool. Observed.
func trialsQueried() eventCheck {
	return eventCheck{name: "recorded trials consulted (trial_query)", fn: func(l *runLog) (bool, string, error) {
		n := 0
		for _, e := range l.ofKind(core.EventToolCall) {
			if payloadString(e, "tool") == "trial_query" {
				n++
			}
		}
		if n > 0 {
			return true, fmt.Sprintf("%d trial_query call(s)", n), nil
		}
		return false, "trial_query was never called", nil
	}}
}

// trialRecorded asserts the agent logged its own trial into the lab --
// continuing the investigation record rather than just consuming it. Observed.
func trialRecorded() eventCheck {
	return eventCheck{name: "a new trial recorded", fn: func(l *runLog) (bool, string, error) {
		if n := len(l.ofKind(core.EventTrialRecorded)); n > 0 {
			return true, fmt.Sprintf("%d trial(s) recorded", n), nil
		}
		for _, e := range l.ofKind(core.EventToolCall) {
			if payloadString(e, "tool") == "trial_record" {
				return true, "tool.call trial_record", nil
			}
		}
		return false, "no trial was recorded", nil
	}}
}

// memoryConsulted asserts the agent actively pulled the scenario's load-bearing
// memory, by any of the three routes that count: a memory_read of it, a recall
// that surfaced it, or the tool call naming it.
func memoryConsulted(name string) eventCheck {
	return eventCheck{name: "load-bearing memory consulted: " + name, fn: func(l *runLog) (bool, string, error) {
		mem, ok, err := store.MemoryByName(l.ctx, l.db, l.project, name)
		if err != nil {
			return false, "", fmt.Errorf("bench: look up memory %s: %w", name, err)
		}
		if !ok {
			return false, fmt.Sprintf("memory %s is not in the run's corpus (seed drift?)", name), nil
		}
		for _, e := range l.ofKind(core.EventMemoryRead) {
			if e.ItemID == mem.ID || payloadString(e, "name") == name {
				return true, "memory.read", nil
			}
		}
		for _, e := range l.ofKind(core.EventInjected) {
			if !strings.HasPrefix(payloadString(e, "source"), "recall") {
				continue
			}
			if slices.Contains(payloadStrings(e, "item_ids"), mem.ID) {
				return true, "surfaced by recall", nil
			}
		}
		for _, e := range l.ofKind(core.EventToolCall) {
			tool := payloadString(e, "tool")
			if tool != "memory_read" && tool != "memory_append" {
				continue
			}
			if args, ok := e.Payload["args"].(map[string]any); ok {
				if s, ok := args["name"].(string); ok && s == name {
					return true, "tool.call " + tool, nil
				}
			}
		}
		injected := 0
		for _, e := range l.ofKind(core.EventInjected) {
			if slices.Contains(payloadStrings(e, "item_ids"), mem.ID) {
				injected++
			}
		}
		return false, fmt.Sprintf("never read or recalled (surfaced in %d injection(s))", injected), nil
	}}
}

// planStepMoved asserts the agent took the plan's claimable step off the queue:
// the task's end state in the DB, with the transition events as corroboration.
func planStepMoved(plan, title string) eventCheck {
	return eventCheck{name: "plan step moved: " + title, fn: func(l *runLog) (bool, string, error) {
		tasks, err := store.ListTasksForPlan(l.ctx, l.db, l.project, "", plan)
		if err != nil {
			return false, "", fmt.Errorf("bench: list plan %s tasks: %w", plan, err)
		}
		idx := slices.IndexFunc(tasks, func(t core.Task) bool { return t.Title == title })
		if idx < 0 {
			return false, fmt.Sprintf("step %q is not in plan %s (seed drift?)", title, plan), nil
		}
		t := tasks[idx]
		transitions := 0
		for _, e := range l.ofKind(core.EventTaskTransition) {
			if e.ItemID == t.ID {
				transitions++
			}
		}
		if t.Status == core.TaskOpen && transitions == 0 {
			return false, "still open, never transitioned", nil
		}
		return true, fmt.Sprintf("status %s after %d transition(s)", t.Status, transitions), nil
	}}
}

// findingRecorded asserts the agent left a durable finding at session_end --
// the handoff the next session inherits.
func findingRecorded() eventCheck {
	return eventCheck{name: "durable finding at session_end", fn: func(l *runLog) (bool, string, error) {
		ended := l.ofKind(core.EventSessionEnded)
		for _, e := range ended {
			if f := payloadString(e, "findings"); strings.TrimSpace(f) != "" {
				return true, fmt.Sprintf("%d chars", len(f)), nil
			}
		}
		if len(ended) == 0 {
			return false, "no session was ended", nil
		}
		return false, fmt.Sprintf("%d session_end(s), all with empty findings", len(ended)), nil
	}}
}

// writesScopedToProject asserts every durable write landed in the scenario's
// project rather than misfiring to global. Global is the empty slug on the
// event (internal/mcp normalizes "global"/"_global" to ""), so a misfire shows
// up as a durable write with no project.
func writesScopedToProject(project string) eventCheck {
	return eventCheck{name: "durable writes scoped to " + project, gate: true, fn: func(l *runLog) (bool, string, error) {
		var writes, misfires int
		offenders := map[string]int{}
		for _, e := range l.Events {
			if e.Kind != core.EventMemoryWritten && e.Kind != core.EventNoteWritten {
				continue
			}
			writes++
			if e.ProjectSlug == project {
				continue
			}
			misfires++
			slug := e.ProjectSlug
			if slug == "" {
				slug = "global"
			}
			offenders[slug]++
		}
		switch {
		case writes == 0:
			return true, "no durable writes", nil
		case misfires == 0:
			return true, fmt.Sprintf("%d write(s), all in %s", writes, project), nil
		default:
			return false, fmt.Sprintf("%d of %d write(s) landed outside %s: %s",
				misfires, writes, project, strings.Join(sortedKeys(offenders), ", ")), nil
		}
	}}
}

// ---------------------------------------------------------------------------
// payload helpers
// ---------------------------------------------------------------------------

func payloadString(e core.Event, key string) string {
	s, _ := e.Payload[key].(string)
	return s
}

func payloadBool(e core.Event, key string) bool {
	b, _ := e.Payload[key].(bool)
	return b
}

// payloadStrings reads a string list out of a payload, which arrives as
// []any after the JSON round-trip through the log.
func payloadStrings(e core.Event, key string) []string {
	switch v := e.Payload[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// normalizeHook reduces a hook marker to its identity, so "session-start" and
// "SessionStart" compare equal.
func normalizeHook(hook string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(hook) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sortedKeys(m map[string]int) []string { return slices.Sorted(maps.Keys(m)) }
