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

// kindRate is a per-kind retrieval row with a derived read-after-inject rate.
type kindRate struct {
	Kind     string `json:"kind"`
	Injects  int    `json:"injects"`
	Reads    int    `json:"reads"`
	ReadRate int    `json:"readRate"`
}

// retrievalData is the payload for the Retrieval page.
type retrievalData struct {
	Injections  int                `json:"injections"`
	Reads       int                `json:"reads"`
	ReadRate    int                `json:"readRate"`
	ByKind      []kindRate         `json:"byKind"`
	Trend       []store.DayCount   `json:"trend"`
	TrendMax    int                `json:"trendMax"`
	TopInjected []store.MemoryStat `json:"topInjected"`
	Stale       []store.MemoryStat `json:"stale"`
	StaleDays   int                `json:"staleDays"`
}

func (s *Service) retrieval(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := store.RebuildRetrievalStats(ctx, s.cfg.DB); err != nil {
		s.logger.Warn("console: rebuild retrieval stats", "error", err)
	}

	sum, err := store.GetUsageSummary(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	byKind, err := store.RetrievalByKind(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	trend, err := store.InjectionsByDay(ctx, s.cfg.DB, 14)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	top, err := store.TopInjectedMemories(ctx, s.cfg.DB, 12)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	stale, err := store.StaleMemories(ctx, s.cfg.DB, time.Now().UTC().AddDate(0, 0, -staleWindowDays))
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	rates := make([]kindRate, 0, len(byKind))
	for _, k := range byKind {
		rates = append(rates, kindRate{
			Kind: k.Kind, Injects: k.Injects, Reads: k.Reads, ReadRate: percent(k.Reads, k.Injects),
		})
	}
	trendMax := 0
	for _, d := range trend {
		if d.Count > trendMax {
			trendMax = d.Count
		}
	}

	s.render(w, r, "retrieval", pageData{
		Title:  "Retrieval",
		Active: "retrieval",
		Data: retrievalData{
			Injections: sum.Retrieval.Injections, Reads: sum.Retrieval.Reads,
			ReadRate: percent(sum.Retrieval.Reads, sum.Retrieval.Injections),
			ByKind:   rates, Trend: trend, TrendMax: trendMax,
			TopInjected: top, Stale: staleStats(stale), StaleDays: staleWindowDays,
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
