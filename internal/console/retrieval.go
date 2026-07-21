package console

import (
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
}

func (s *Service) retrieval(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := store.RebuildRetrievalStats(ctx, s.cfg.DB); err != nil {
		s.logger.Warn("console: rebuild retrieval stats", "error", err)
	}

	win := store.ResolveRetrievalWindow(r.URL.Query().Get("w"), time.Now())
	report, err := store.BuildRetrievalReport(ctx, s.cfg.DB, win, 12)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	stale, err := store.StaleMemories(ctx, s.cfg.DB, time.Now().UTC().AddDate(0, 0, -staleWindowDays))
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
			ToolMisses: report.ToolMisses,
			DeadWeight: report.DeadWeight,
		},
	})
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
