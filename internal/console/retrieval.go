package console

import (
	"context"
	"net/http"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// staleWindowDays is how long an active memory can go without being updated,
// injected, or read before the Retrieval page flags it as stale. It mirrors the
// gardener's default staleness horizon.
const staleWindowDays = 90

// retrievalData is the payload for the Retrieval page. The reach funnel, by-kind,
// trend, and top lists are all computed over the selected window; Stale is
// window-independent.
type retrievalData struct {
	Injections       int                  `json:"injections"`
	InjectedTokens   int                  `json:"injectedTokens"`
	MemoriesSurfaced int                  `json:"memoriesSurfaced"`
	ActiveMemories   int                  `json:"activeMemories"`
	ReachRate        int                  `json:"reachRate"`
	SessionsReached  int                  `json:"sessionsReached"`
	CreatedInWindow  int                  `json:"createdInWindow"`
	RetiredInWindow  int                  `json:"retiredInWindow"`
	Window           string               `json:"window"`
	WindowLabel      string               `json:"windowLabel"`
	Windows          []windowOption       `json:"-"`
	ByKind           []store.KindReach    `json:"byKind"`
	ByProject        []store.ProjectReach `json:"byProject"`
	Trend            []store.TrendBucket  `json:"trend"`
	TrendMax         int                  `json:"trendMax"`
	TopInjected      []store.MemoryStat   `json:"topInjected"`
	Stale            []store.MemoryStat   `json:"stale"`
	StaleDays        int                  `json:"staleDays"`

	// Loop health (closed-loop retrieval): push vs pull, and its token cost.
	BriefingSurfaced   int                `json:"briefingSurfaced"`
	DemandedOfSurfaced int                `json:"demandedOfSurfaced"`
	DemandRate         int                `json:"demandRate"`
	WasteShare         int                `json:"wasteShare"`
	PromptMatches      int                `json:"promptMatches"`
	RecallMisses       int                `json:"recallMisses"`
	MissRate           int                `json:"missRate"`
	ToolMisses         int                `json:"toolMisses"`
	DeadWeight         []store.MemoryStat `json:"deadWeight"`

	// Read-after-inject funnel segmented by injection surface: window-scoped
	// injections, conversions within FunnelFollowHours of each injection.
	FunnelBySurface   []store.SurfaceFunnel `json:"funnelBySurface"`
	FunnelFollowHours int                   `json:"funnelFollowHours"`

	// Judgment: what each headline number did against the equally long window
	// before it, and the threshold it is measured against. The loop-health rates
	// carry a band without a delta -- their prior-window comparative is not
	// derivable from the bounded rollup, and a stated ceiling judges them
	// honestly where an invented delta would not.
	PriorLabel      string `json:"-"` // "vs prior 7d"; empty when there is no prior window
	ReachDelta      delta  `json:"-"`
	ReachBand       band   `json:"-"`
	SurfacedOf      delta  `json:"-"` // "of N active" -- a denominator, not a movement
	InjectionsDelta delta  `json:"-"`
	SessionsDelta   delta  `json:"-"`
	VolumeBand      band   `json:"-"` // what the volume chips compare against
	DemandBand      band   `json:"-"`
	WasteBand       band   `json:"-"`
	MissBand        band   `json:"-"`
}

func (s *Service) retrieval(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := store.RebuildRetrievalStats(ctx, s.cfg.DB); err != nil {
		s.logger.Warn("console: rebuild retrieval stats", "error", err)
	}

	now := time.Now()
	win := store.ResolveRetrievalWindow(r.URL.Query().Get("w"), now)
	report, err := store.BuildRetrievalReport(ctx, s.cfg.DB, win, 12)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	prior, hasPrior := s.priorVitals(ctx, win, now)
	stale, err := store.StaleMemories(ctx, s.cfg.DB, time.Now().UTC().AddDate(0, 0, -staleWindowDays))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	funnel, err := store.ReadAfterInjectFunnel(ctx, s.cfg.DB, win.Since, 0)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	trendMax := 0
	for _, b := range report.Trend {
		if b.Count > trendMax {
			trendMax = b.Count
		}
	}

	s.render(w, r, "retrieval", pageData{
		Title:  "Retrieval",
		Active: "retrieval",
		Data: retrievalData{
			Injections: report.Injected, InjectedTokens: report.InjectedTokens,
			MemoriesSurfaced: report.MemoriesSurfaced,
			ActiveMemories:   report.ActiveMemories, ReachRate: report.ReachRate,
			SessionsReached: report.SessionsReached,
			CreatedInWindow: report.CreatedInWindow, RetiredInWindow: report.RetiredInWindow,
			Window: win.Key, WindowLabel: win.Label, Windows: windowOptions(win.Key),
			ByKind: report.ByKind, ByProject: report.ByProject, Trend: report.Trend, TrendMax: trendMax,
			TopInjected: report.Top, Stale: staleStats(stale), StaleDays: staleWindowDays,
			BriefingSurfaced: report.BriefingSurfaced, DemandedOfSurfaced: report.DemandedOfSurfaced,
			DemandRate: report.DemandRate, WasteShare: report.WasteShare,
			PromptMatches: report.PromptMatches, RecallMisses: report.RecallMisses, MissRate: report.MissRate,
			ToolMisses:        report.ToolMisses,
			DeadWeight:        report.DeadWeight,
			FunnelBySurface:   funnel,
			FunnelFollowHours: int(store.DefaultFunnelFollow.Hours()),

			PriorLabel:      priorLabel(win, hasPrior),
			ReachDelta:      pointDelta(report.ReachRate, prior.ReachRate, hasPrior && report.ActiveMemories > 0, riseGood),
			ReachBand:       floorBand(report.ReachRate, reachTargetPct, report.ActiveMemories > 0),
			SurfacedOf:      ofDelta(report.ActiveMemories),
			InjectionsDelta: volumeDelta(report.Injected, prior.Injections, hasPrior, noJudgment),
			SessionsDelta:   volumeDelta(report.SessionsReached, prior.SessionsReached, hasPrior, noJudgment),
			VolumeBand:      noteBand(priorLabel(win, hasPrior)),
			DemandBand:      floorBand(report.DemandRate, demandTargetPct, report.BriefingSurfaced > 0),
			WasteBand:       ceilingBand(report.WasteShare, deadWeightCeilingPct, report.InjectedTokens > 0),
			MissBand:        ceilingBand(report.MissRate, missCeilingPct, report.RecallMisses+report.PromptMatches > 0),
		},
	})
}

// priorVitals loads the equally long window before win, for the delta chips. A
// missing or failed prior window degrades to "no comparison" -- an absent chip
// is honest, a zeroed one is not -- so a query failure warns and continues
// rather than failing the page.
func (s *Service) priorVitals(ctx context.Context, win store.RetrievalWindow, now time.Time) (store.WindowVitals, bool) {
	since, until, ok := store.PriorWindow(win, now)
	if !ok {
		return store.WindowVitals{}, false
	}
	v, err := store.GetWindowVitals(ctx, s.cfg.DB, since, until)
	if err != nil {
		s.logger.Warn("console: prior-window vitals", "window", win.Key, "error", err)
		return store.WindowVitals{}, false
	}
	return v, true
}

// priorProjectVitals is priorVitals scoped to one project, for the workspace's
// reach and continuity chips. Same degradation: no prior window, or a failed
// query, means no chip rather than a zeroed one.
func (s *Service) priorProjectVitals(ctx context.Context, project string, win store.RetrievalWindow, now time.Time) (store.WindowVitals, bool) {
	since, until, ok := store.PriorWindow(win, now)
	if !ok {
		return store.WindowVitals{}, false
	}
	v, err := store.GetWindowVitalsForProject(ctx, s.cfg.DB, project, since, until)
	if err != nil {
		s.logger.Warn("console: prior-window vitals", "project", project, "window", win.Key, "error", err)
		return store.WindowVitals{}, false
	}
	return v, true
}

// priorLabel names the comparison a delta chip is against ("vs prior 7d"), or
// returns "" when the selected window has none to compare with.
func priorLabel(win store.RetrievalWindow, has bool) string {
	if !has {
		return ""
	}
	return "vs prior " + win.Label
}

// staleStats projects stale memories into MemoryStat for uniform rendering.
// Stale by definition means no recent injection/read, so only the updated age
// carries information.
func staleStats(mems []core.Memory) []store.MemoryStat {
	out := make([]store.MemoryStat, 0, len(mems))
	for _, m := range mems {
		out = append(out, store.MemoryStat{
			ID: m.ID, Name: m.Name, Kind: string(m.Kind), Project: m.Project, Updated: m.Updated,
		})
	}
	return out
}
