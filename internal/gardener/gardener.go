// Package gardener runs the propose-only maintenance passes over the memory
// corpus: it finds near-duplicate memories (dedup), memories that have gone
// untouched for too long (staleness), and rolls recent sessions into a monthly
// digest. Every pass only ever writes a gardener_proposals row for the owner to
// apply or dismiss -- it never mutates a memory on its own. The passes run on a
// ticker and are also invokable on demand (RunOnce).
package gardener

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/llm"
	"github.com/0spoon/seamless/internal/store"
)

// Default pass parameters, used when a Config field is non-positive.
const (
	defaultDedupThreshold = 0.88
	defaultStalenessDays  = 90
	defaultDigestDays     = 30
	defaultInterval       = time.Hour
	// minDigestSessions is the fewest completed sessions in a project's window
	// worth rolling into a digest (a single session is already its own finding).
	minDigestSessions = 2
)

// Config parameterizes the gardener passes.
type Config struct {
	DedupThreshold float64
	StalenessDays  int
	DigestDays     int
	Interval       time.Duration
}

// withDefaults fills non-positive fields from the package defaults.
func (c Config) withDefaults() Config {
	if c.DedupThreshold <= 0 {
		c.DedupThreshold = defaultDedupThreshold
	}
	if c.StalenessDays <= 0 {
		c.StalenessDays = defaultStalenessDays
	}
	if c.DigestDays <= 0 {
		c.DigestDays = defaultDigestDays
	}
	if c.Interval <= 0 {
		c.Interval = defaultInterval
	}
	return c
}

// FromConfig adapts the server config's gardener block to the pass Config.
func FromConfig(g config.Gardener) Config {
	return Config{
		DedupThreshold: g.DedupThreshold,
		StalenessDays:  g.StalenessDays,
		DigestDays:     g.DigestDays,
		Interval:       time.Duration(g.IntervalMinutes) * time.Minute,
	}
}

// Service runs the gardener passes over one store.
type Service struct {
	db       *sql.DB
	files    *files.Manager
	embedder llm.Embedder // nil => dedup pass is skipped (no vectors to compare)
	chat     llm.Chat     // nil => digest pass is skipped (no summarizer)
	events   *events.Recorder
	cfg      Config
	logger   *slog.Logger
	now      func() time.Time // injectable clock (tests)
}

// New builds a gardener Service. embedder and chat may each be nil, disabling
// the pass that needs them (dedup and digest respectively); staleness always
// runs. events may be nil (proposal telemetry is then skipped).
func New(db *sql.DB, mgr *files.Manager, embedder llm.Embedder, chat llm.Chat, rec *events.Recorder, cfg Config, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		db: db, files: mgr, embedder: embedder, chat: chat, events: rec,
		cfg: cfg.withDefaults(), logger: logger, now: time.Now,
	}
}

// PassResult counts what a single RunOnce produced.
type PassResult struct {
	Merges   int `json:"merges"`
	Archives int `json:"archives"`
	Digests  int `json:"digests"`
}

// Total is the number of proposals created across all passes.
func (r PassResult) Total() int { return r.Merges + r.Archives + r.Digests }

// RunOnce refreshes retrieval stats, then runs all three propose-only passes and
// returns what each created. A pass that fails is logged and skipped; a single
// failing pass never aborts the others.
func (s *Service) RunOnce(ctx context.Context) (PassResult, error) {
	// Staleness reads last-injected/last-read from retrieval_stats, a projection
	// of the event log; refresh it first so this pass sees current activity.
	if err := store.RebuildRetrievalStats(ctx, s.db); err != nil {
		s.logger.Warn("gardener: rebuild retrieval stats", "error", err)
	}

	existing, err := store.AllProposalKeys(ctx, s.db)
	if err != nil {
		return PassResult{}, err
	}

	var res PassResult
	if n, err := s.proposeMerges(ctx, existing); err != nil {
		s.logger.Warn("gardener: dedup pass", "error", err)
	} else {
		res.Merges = n
	}
	if n, err := s.proposeArchives(ctx, existing); err != nil {
		s.logger.Warn("gardener: staleness pass", "error", err)
	} else {
		res.Archives = n
	}
	if n, err := s.proposeDigests(ctx, existing); err != nil {
		s.logger.Warn("gardener: digest pass", "error", err)
	} else {
		res.Digests = n
	}

	if res.Total() > 0 {
		s.logger.Info("gardener pass complete", "merges", res.Merges, "archives", res.Archives, "digests", res.Digests)
	}
	return res, nil
}

// createProposal persists a proposal and records a gardener.action event. The
// key is added to seen so a single run does not propose the same thing twice.
func (s *Service) createProposal(ctx context.Context, kind, key string, payload map[string]any, seen map[string]struct{}) error {
	payload["key"] = key
	p, err := store.CreateProposal(ctx, s.db, kind, payload)
	if err != nil {
		return err
	}
	seen[key] = struct{}{}
	s.record(ctx, p.ID, map[string]any{"action": "propose", "kind": kind, "key": key})
	return nil
}

// record appends a gardener.action event best-effort.
func (s *Service) record(ctx context.Context, itemID string, payload map[string]any) {
	if s.events == nil {
		return
	}
	if _, err := s.events.Record(ctx, core.Event{
		Kind: core.EventGardenerAction, ItemID: itemID, Payload: payload,
	}); err != nil {
		s.logger.Warn("gardener: record event", "error", err)
	}
}
