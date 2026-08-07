package console

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/features"
)

// eventRow is a display-ready projection of one event-log entry.
type eventRow struct {
	ID        string    `json:"id"`
	When      time.Time `json:"ts"`
	Kind      string    `json:"kind"`
	Project   string    `json:"project,omitempty"`
	SessionID string    `json:"sessionId,omitempty"`
	ItemID    string    `json:"itemId,omitempty"`
	Summary   string    `json:"summary"`
}

// recentEvents returns the most recent events as display rows.
func (s *Service) recentEvents(ctx context.Context, limit int) ([]eventRow, error) {
	if s.cfg.Events == nil {
		return nil, nil
	}
	// Hide transport-level Interactions noise (tool.call/hook.prompt) from the
	// overview's business feed; those live on the Interactions screen instead.
	evs, err := s.cfg.Events.RecentExcluding(ctx, limit, core.EventToolCall, core.EventHookPrompt)
	if err != nil {
		return nil, err
	}
	out := make([]eventRow, 0, len(evs))
	for _, e := range evs {
		out = append(out, toEventRow(e))
	}
	return out, nil
}

// recentMishaps returns the latest agent-reported mishaps as recurrence-review
// rows for the overview rail, newest first. Each row is attributed to the
// reporting session's harness and model so the report traces to the exact task.
func (s *Service) recentMishaps(ctx context.Context, limit int) ([]mishapRow, error) {
	if s.cfg.Events == nil {
		return nil, nil
	}
	evs, err := s.cfg.Events.ByKinds(ctx, []core.EventKind{core.EventAgentMishap}, "", "", limit)
	if err != nil {
		return nil, err
	}
	sessOf := s.sourceSessionResolver(ctx)
	out := make([]mishapRow, 0, len(evs))
	for _, e := range evs {
		row := mishapRow{
			ID:          e.ID,
			When:        e.TS,
			Project:     e.ProjectSlug,
			SessionID:   e.SessionID,
			Description: snippet(payloadStr(e.Payload, "description"), 160),
		}
		if e.SessionID != "" {
			sess := sessOf(e.SessionID)
			row.Harness, row.Model = harnessOf(sess), sess.Model
		}
		out = append(out, row)
	}
	return out, nil
}

func toEventRow(e core.Event) eventRow {
	return eventRow{
		ID:        e.ID,
		When:      e.TS,
		Kind:      string(e.Kind),
		Project:   e.ProjectSlug,
		SessionID: e.SessionID,
		ItemID:    e.ItemID,
		Summary:   eventSummary(e),
	}
}

// eventSummary renders a one-line human description of an event from its payload.
func eventSummary(e core.Event) string {
	p := e.Payload
	switch e.Kind {
	case core.EventSessionStarted:
		if ambient, _ := p["ambient"].(bool); ambient {
			return "ambient session started"
		}
		return "session started"
	case core.EventSessionEnded:
		return "session ended"
	case core.EventMemoryWritten:
		return "wrote memory " + payloadStr(p, "name")
	case core.EventMemoryRead:
		return "read memory " + payloadStr(p, "name")
	case core.EventMemorySuperseded:
		return "superseded " + payloadStr(p, "name")
	case core.EventMemoryArchived:
		return "archived " + payloadStr(p, "name")
	case core.EventProjectIsolationChanged:
		line := "isolation " + payloadStr(p, "from") + " -> " + payloadStr(p, "to")
		if parent := payloadStr(p, "parent"); parent != "" {
			line += "; detached from " + parent
		}
		return line
	case core.EventRepoMoved:
		if path := payloadStr(p, "new_path"); path != "" {
			return "repo moved to " + path + "; project adopted"
		}
		return "repo moved; project adopted"
	case core.EventNoteWritten:
		return "wrote note " + payloadStr(p, "title")
	case core.EventNoteRead:
		// A read names its note by slug rather than the title the write side
		// records: the tool takes an id or a slug, so the title is not
		// necessarily in hand when the event is written.
		if slug := payloadStr(p, "slug"); slug != "" {
			return "read note " + slug
		}
		return "read note"
	case core.EventFavoriteChanged:
		verb := "starred"
		if fav, _ := p["favorite"].(bool); !fav {
			verb = "unstarred"
		}
		return verb + " " + payloadStr(p, "kind") + " " + payloadStr(p, "id")
	case core.EventTrialRecorded:
		return "recorded trial " + payloadStr(p, "title")
	case eventFeaturesChanged:
		if reset, _ := p["reset"].(bool); reset {
			return "optional features reset to the file configuration"
		}
		return "optional features changed" + featureStateSuffix(p)
	case core.EventTaskTransition:
		if to := payloadStr(p, "to"); to != "" {
			return "task -> " + to
		}
		return "task transition"
	case core.EventInjected:
		if hook := payloadStr(p, "hook"); hook != "" {
			return hook + " injection"
		}
		return "context injected"
	case core.EventGardenerAction:
		action := payloadStr(p, "action")
		if kind := payloadStr(p, "kind"); kind != "" {
			return fmt.Sprintf("gardener %s (%s)", action, kind)
		}
		return "gardener " + action
	case core.EventToolCall:
		label := "tool " + payloadStr(p, "tool")
		if isErr, _ := p["is_error"].(bool); isErr {
			label += " failed"
		}
		return label
	case core.EventHookPrompt:
		return "prompt (no recall match)"
	case core.EventAgentMishap:
		return "mishap: " + snippet(payloadStr(p, "description"), 120)
	case core.EventPlanCaptured:
		s := "captured plan " + payloadStr(p, "basename")
		if it, ok := p["iteration"].(float64); ok {
			s += fmt.Sprintf(" (iter %d)", int(it))
		}
		return s
	case core.EventPlanPresented:
		return "presented plan " + payloadStr(p, "basename")
	case core.EventPlanApproved:
		return "approved plan " + payloadStr(p, "basename")
	case core.EventSubagentCaptured:
		if at := payloadStr(p, "agent_type"); at != "" {
			return "cached subagent (" + at + ")"
		}
		return "cached subagent"
	default:
		return string(e.Kind)
	}
}

func payloadStr(p map[string]any, key string) string {
	if p == nil {
		return ""
	}
	if v, ok := p[key].(string); ok {
		return v
	}
	return ""
}

// featureStateSuffix renders a features_changed payload's per-feature state as
// " (research on, ... off)", in registry order so the line reads the same way
// every time. An empty or unreadable map contributes nothing rather than an
// invented state.
func featureStateSuffix(p map[string]any) string {
	state := payloadMap(p, "features")
	if len(state) == 0 {
		return ""
	}
	var parts []string
	for _, f := range features.Registry() {
		on, ok := state[string(f.Key)].(bool)
		if !ok {
			continue
		}
		word := "off"
		if on {
			word = "on"
		}
		parts = append(parts, string(f.Key)+" "+word)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// payloadMap reads a nested object field from a payload map (nil if absent).
func payloadMap(p map[string]any, key string) map[string]any {
	if p == nil {
		return nil
	}
	if v, ok := p[key].(map[string]any); ok {
		return v
	}
	return nil
}

// payloadList reads an array-of-objects field from a payload map (nil if absent),
// e.g. a consolidate proposal's "sources".
func payloadList(p map[string]any, key string) []map[string]any {
	if p == nil {
		return nil
	}
	raw, ok := p[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if obj, ok := v.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}
