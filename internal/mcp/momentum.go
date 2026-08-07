package mcp

import (
	"context"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/features"
)

// markFirstReuse records the momentum "first reuse" moment: the first time a
// memory is read by a session other than the one that wrote it -- the point
// where stored knowledge visibly pays off. Latched once per memory ever via
// events.RecordOnce, and gated on the momentum feature so a disabled install
// records nothing and behaves exactly as before. Best-effort by design: a
// failure here must never fail the read that triggered it.
func (s *Server) markFirstReuse(ctx context.Context, mem core.Memory) {
	if s.cfg.Events == nil || s.cfg.DB == nil {
		return
	}
	if !features.Enabled(s.effectiveFeatures(ctx), features.Momentum) {
		return
	}
	// Reuse needs a known writer and a different, known reader. source_session
	// carries the writer's session ULID as this server stamps it -- but v1
	// imports carry session NAMES there, so the reader matches on either
	// identity before the moment counts as someone else's.
	if mem.SourceSession == "" {
		return
	}
	readerID := s.boundSession(ctx)
	if readerID == "" || readerID == mem.SourceSession {
		return
	}
	reader := s.boundSessionName(ctx)
	if reader == mem.SourceSession {
		return
	}
	if reader == "" {
		reader = readerID
	}
	_, recorded, err := s.cfg.Events.RecordOnce(ctx, core.Event{
		Kind: core.EventMemoryFirstReuse, SessionID: readerID,
		ProjectSlug: mem.Project, ItemID: mem.ID,
		Payload: map[string]any{
			"name": mem.Name, "kind": string(mem.Kind),
			"writer": mem.SourceSession, "reader": reader,
		},
	})
	if err != nil {
		s.logger.Warn("momentum: first-reuse mark", "memory", mem.Name, "error", err)
		return
	}
	if recorded {
		s.logger.Info("momentum: first reuse", "memory", mem.Name, "reader", reader)
	}
}
