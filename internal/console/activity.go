package console

import (
	"context"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
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
	evs, err := s.cfg.Events.Recent(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]eventRow, 0, len(evs))
	for _, e := range evs {
		out = append(out, toEventRow(e))
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
	case core.EventNoteWritten:
		return "wrote note " + payloadStr(p, "title")
	case core.EventTrialRecorded:
		return "recorded trial " + payloadStr(p, "title")
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
		return "tool " + payloadStr(p, "tool")
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
