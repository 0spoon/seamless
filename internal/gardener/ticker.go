package gardener

import (
	"context"
	"time"
)

const (
	// initialDelay lets the server finish starting before the first pass runs, so
	// startup latency never waits on gardening.
	initialDelay = 20 * time.Second
	// passTimeout bounds a single pass; the digest step may call an LLM, so it is
	// generous relative to the pass cadence.
	passTimeout = 5 * time.Minute
)

// Start launches the gardener ticker in a background goroutine: one pass shortly
// after startup, then every Interval, until ctx is cancelled. It never blocks the
// caller and never panics the server (each pass is best-effort). Call it only
// when the gardener is enabled.
func (s *Service) Start(ctx context.Context) {
	go s.loop(ctx)
}

func (s *Service) loop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(initialDelay):
	}
	s.runOnceLogged(ctx)

	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOnceLogged(ctx)
		}
	}
}

// runOnceLogged runs a single pass under a bounded timeout, logging any failure
// rather than propagating it (the ticker must keep running).
func (s *Service) runOnceLogged(ctx context.Context) {
	passCtx, cancel := context.WithTimeout(ctx, passTimeout)
	defer cancel()
	if _, err := s.RunOnce(passCtx); err != nil {
		s.logger.Warn("gardener: pass failed", "error", err)
	}
}
