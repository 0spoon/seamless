package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// RetrievalWindowKeys are the selectable trailing windows for the retrieval-health
// views, in display order. "all" (the default) spans every recorded event.
var RetrievalWindowKeys = []string{"24h", "7d", "30d", "all"}

// RetrievalWindow is a resolved trailing time window for the retrieval-health
// views: a stable key (URL + selector), a human label, and the inclusive lower
// bound. A zero Since means "all time" (unbounded).
type RetrievalWindow struct {
	Key   string    `json:"key"`
	Label string    `json:"label"`
	Since time.Time `json:"since"`
}

// ResolveRetrievalWindow maps a selector key to a window anchored at now. Unknown
// or empty keys fall back to "all", the default.
func ResolveRetrievalWindow(key string, now time.Time) RetrievalWindow {
	switch key {
	case "24h":
		return RetrievalWindow{Key: "24h", Label: "24h", Since: now.Add(-24 * time.Hour)}
	case "7d":
		return RetrievalWindow{Key: "7d", Label: "7d", Since: now.AddDate(0, 0, -7)}
	case "30d":
		return RetrievalWindow{Key: "30d", Label: "30d", Since: now.AddDate(0, 0, -30)}
	default:
		return RetrievalWindow{Key: "all", Label: "all time"}
	}
}

// TrendBucket is one time bucket of the injection trend: a pre-formatted tick
// label and the item-level injection count that fell in it. The report's total
// Injected equals the sum of bucket counts, so the hero number and the chart
// always describe the same quantity over the same window.
type TrendBucket struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// KindReach is one memory kind's share of a window's injection activity: total
// injections (volume) and the number of distinct active memories of that kind
// that were surfaced at least once.
type KindReach struct {
	Kind     string `json:"kind"`
	Injects  int    `json:"injects"`
	Memories int    `json:"memories"`
}

// ProjectReach is one project's reach within the window: how many of its active
// memories were surfaced at least once, out of its total active memories, plus
// the injection volume attributable to them. It lets the global reach number be
// read per knowledge base -- a project whose memories never surface shows 0%
// even while the global rate looks healthy. Project "" is the global scope.
type ProjectReach struct {
	Project   string `json:"project"`
	Surfaced  int    `json:"surfaced"`  // distinct active memories of this project surfaced
	Active    int    `json:"active"`    // total active memories in this project (reach denominator)
	ReachRate int    `json:"reachRate"` // Surfaced / Active, rounded %
	Injects   int    `json:"injects"`   // injection volume attributable to this project's memories
}

// RetrievalReport is the retrieval-REACH rollup for a window, computed live from
// the injection event stream. It measures how far the knowledge base actually
// reaches agents -- how many distinct memories get surfaced, across how many
// sessions -- rather than a read-back rate. (Read-after-inject is not tracked
// here: agents almost never memory_read what a briefing already surfaced, so the
// only honest consumption signal for this system is reach, not re-reads.)
type RetrievalReport struct {
	Window           RetrievalWindow `json:"window"`
	Injected         int             `json:"injected"`         // item-level injections in the window (volume) == sum(Trend)
	MemoriesSurfaced int             `json:"memoriesSurfaced"` // distinct active memories surfaced >=1x
	ActiveMemories   int             `json:"activeMemories"`   // total active memories (reach denominator)
	ReachRate        int             `json:"reachRate"`        // MemoriesSurfaced / ActiveMemories, rounded %
	SessionsReached  int             `json:"sessionsReached"`  // distinct sessions that received >=1 injection
	ByKind           []KindReach     `json:"byKind"`
	ByProject        []ProjectReach  `json:"byProject"` // reach sliced per project (denominator = that project's active memories)
	Top              []MemoryStat    `json:"top"`       // most-injected active memories, with sessions reached
	Trend            []TrendBucket   `json:"trend"`
	Hourly           bool            `json:"hourly"` // trend granularity: hourly (24h window) vs daily
}

// injection is one (session, memory) injection occurrence within the window.
type injection struct {
	session string // session_id, or the payload's claude_session_id when the column is empty
	item    string
	at      time.Time
}

// memMeta is the naming/classification an injected id needs to appear in the
// per-kind and most-injected breakdowns.
type memMeta struct{ kind, name, project string }

// BuildRetrievalReport computes the windowed retrieval-reach rollup in a single
// pass over the injection event stream: total injection volume, how many distinct
// active memories were surfaced (reach), across how many sessions, plus the
// per-kind and most-injected breakdowns and the injection trend. topN caps the
// most-injected list (<=0 => 12). Archived/unknown injected ids still count
// toward the volume total and the trend, but drop out of the by-kind and
// most-injected breakdowns (scoped to active memories).
func BuildRetrievalReport(ctx context.Context, db *sql.DB, w RetrievalWindow, topN int) (RetrievalReport, error) {
	if topN <= 0 {
		topN = 12
	}
	rep := RetrievalReport{Window: w, Hourly: w.Key == "24h"}

	meta, err := activeMemoryMeta(ctx, db)
	if err != nil {
		return rep, err
	}
	rep.ActiveMemories = len(meta)

	// Active-memory denominator per project (the reach denominator is per project,
	// so a project with zero surfaced memories still appears at 0%).
	activeByProject := map[string]int{}
	for _, m := range meta {
		activeByProject[m.project]++
	}

	args := []any{string(core.EventInjected)}
	q := `SELECT ts, session_id, item_id, payload FROM events WHERE kind = ?`
	if !w.Since.IsZero() {
		q += ` AND ts >= ?`
		args = append(args, core.FormatTime(w.Since))
	}
	q += ` ORDER BY ts ASC, id ASC`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return rep, fmt.Errorf("store.BuildRetrievalReport: query events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var injections []injection
	sessions := map[string]struct{}{} // distinct sessions reached (any injection)
	var earliest time.Time
	for rows.Next() {
		var tsStr, sessionID, itemID, payload string
		if err := rows.Scan(&tsStr, &sessionID, &itemID, &payload); err != nil {
			return rep, fmt.Errorf("store.BuildRetrievalReport: scan: %w", err)
		}
		ts, err := core.ParseTime(tsStr)
		if err != nil {
			return rep, fmt.Errorf("store.BuildRetrievalReport: parse ts: %w", err)
		}
		sess := injectedSessionKey(sessionID, payload)
		if sess != "" {
			sessions[sess] = struct{}{}
		}
		for _, id := range injectedItemIDs(itemID, payload) {
			injections = append(injections, injection{session: sess, item: id, at: ts})
			if earliest.IsZero() || ts.Before(earliest) {
				earliest = ts
			}
		}
	}
	if err := rows.Err(); err != nil {
		return rep, fmt.Errorf("store.BuildRetrievalReport: rows: %w", err)
	}
	rep.SessionsReached = len(sessions)

	// Per-kind (distinct memories) and per-memory (distinct sessions) rollups over
	// active memories only; the volume total covers every injected id.
	kindMems := map[string]map[string]struct{}{} // kind -> set of memory ids
	kindInj := map[string]int{}
	projMems := map[string]map[string]struct{}{} // project -> set of surfaced memory ids
	projInj := map[string]int{}
	memInj := map[string]int{}
	memSess := map[string]map[string]struct{}{} // memory id -> set of sessions
	for _, in := range injections {
		rep.Injected++
		m, known := meta[in.item]
		if !known {
			continue
		}
		kindInj[m.kind]++
		if kindMems[m.kind] == nil {
			kindMems[m.kind] = map[string]struct{}{}
		}
		kindMems[m.kind][in.item] = struct{}{}
		projInj[m.project]++
		if projMems[m.project] == nil {
			projMems[m.project] = map[string]struct{}{}
		}
		projMems[m.project][in.item] = struct{}{}
		memInj[in.item]++
		if memSess[in.item] == nil {
			memSess[in.item] = map[string]struct{}{}
		}
		if in.session != "" {
			memSess[in.item][in.session] = struct{}{}
		}
	}
	rep.MemoriesSurfaced = len(memInj)
	if rep.ActiveMemories > 0 {
		rep.ReachRate = int(float64(rep.MemoriesSurfaced)/float64(rep.ActiveMemories)*100 + 0.5)
	}

	rep.ByKind = make([]KindReach, 0, len(kindInj))
	for kind, inj := range kindInj {
		rep.ByKind = append(rep.ByKind, KindReach{Kind: kind, Injects: inj, Memories: len(kindMems[kind])})
	}
	sort.Slice(rep.ByKind, func(i, j int) bool {
		if rep.ByKind[i].Injects != rep.ByKind[j].Injects {
			return rep.ByKind[i].Injects > rep.ByKind[j].Injects
		}
		return rep.ByKind[i].Kind < rep.ByKind[j].Kind
	})

	// Every project with active memories appears (largest knowledge base first),
	// including those at 0% reach -- that gap is the point of the breakdown.
	rep.ByProject = make([]ProjectReach, 0, len(activeByProject))
	for proj, active := range activeByProject {
		surfaced := len(projMems[proj])
		rate := 0
		if active > 0 {
			rate = int(float64(surfaced)/float64(active)*100 + 0.5)
		}
		rep.ByProject = append(rep.ByProject, ProjectReach{
			Project: proj, Surfaced: surfaced, Active: active, ReachRate: rate, Injects: projInj[proj],
		})
	}
	sort.Slice(rep.ByProject, func(i, j int) bool {
		if rep.ByProject[i].Active != rep.ByProject[j].Active {
			return rep.ByProject[i].Active > rep.ByProject[j].Active
		}
		if rep.ByProject[i].Surfaced != rep.ByProject[j].Surfaced {
			return rep.ByProject[i].Surfaced > rep.ByProject[j].Surfaced
		}
		return rep.ByProject[i].Project < rep.ByProject[j].Project
	})

	rep.Top = make([]MemoryStat, 0, len(memInj))
	for id, inj := range memInj {
		m := meta[id]
		rep.Top = append(rep.Top, MemoryStat{
			ID: id, Name: m.name, Kind: m.kind, Project: m.project,
			Injects: inj, Sessions: len(memSess[id]),
		})
	}
	sort.Slice(rep.Top, func(i, j int) bool {
		if rep.Top[i].Injects != rep.Top[j].Injects {
			return rep.Top[i].Injects > rep.Top[j].Injects
		}
		return rep.Top[i].ID < rep.Top[j].ID
	})
	if len(rep.Top) > topN {
		rep.Top = rep.Top[:topN]
	}

	rep.Trend = buildTrend(injections, w, rep.Hourly, earliest, time.Now())
	return rep, nil
}

// injectedSessionKey resolves the session an injection belongs to: the event's
// session_id column when set, else the payload's claude_session_id (older
// briefing injections were recorded before the ambient session was linked, so
// their column is empty but the claude id is always in the payload).
func injectedSessionKey(sessionID, payload string) string {
	if sessionID != "" {
		return sessionID
	}
	if payload == "" || payload == "{}" {
		return ""
	}
	var p injectedPayload
	if err := json.Unmarshal([]byte(payload), &p); err == nil {
		return p.ClaudeSessionID
	}
	return ""
}

// buildTrend densifies the window into contiguous local-time buckets (so "today"
// is the local day, never a UTC-rollover day into tomorrow) and tallies
// item-level injections into them. For an all-time window the range starts at the
// earliest injection. Returns nil when the window holds no injections.
func buildTrend(injections []injection, w RetrievalWindow, hourly bool, earliest, now time.Time) []TrendBucket {
	var start time.Time
	switch {
	case !w.Since.IsZero():
		start = w.Since
	case !earliest.IsZero():
		start = earliest
	default:
		return nil
	}
	counts := make(map[string]int, len(injections))
	for _, in := range injections {
		counts[bucketKey(in.at, hourly)]++
	}
	axis := localBucketAxis(start, now, hourly)
	out := make([]TrendBucket, 0, len(axis))
	for _, t := range axis {
		out = append(out, TrendBucket{Label: t.label, Count: counts[t.key]})
	}
	return out
}

// bucketTick is one contiguous trend bucket: its map key (for tallying) and its
// pre-formatted x-axis label.
type bucketTick struct{ key, label string }

// localBucketAxis returns the contiguous local-time buckets spanning [start, now],
// hourly or daily, in order. The injection and coverage trends share it so their
// x-axes line up over the same window.
func localBucketAxis(start, now time.Time, hourly bool) []bucketTick {
	start, end := start.Local(), now.Local()
	var labelFn func(time.Time) string
	var floor, next func(time.Time) time.Time
	if hourly {
		labelFn = func(t time.Time) string { return t.Format("15:04") }
		floor = func(t time.Time) time.Time {
			return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
		}
		next = func(t time.Time) time.Time { return t.Add(time.Hour) }
	} else {
		labelFn = func(t time.Time) string { return t.Format("Jan 02") }
		floor = func(t time.Time) time.Time {
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
		}
		next = func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
	}
	var out []bucketTick
	for cur := floor(start); !cur.After(end); cur = next(cur) {
		out = append(out, bucketTick{key: bucketKey(cur, hourly), label: labelFn(cur)})
	}
	return out
}

// bucketKey is the local-time bucket key for t at the given granularity; it must
// match the keys localBucketAxis emits so tallies land in the right bucket.
func bucketKey(t time.Time, hourly bool) string {
	if hourly {
		return t.Local().Format("2006-01-02T15")
	}
	return t.Local().Format("2006-01-02")
}

// activeMemoryMeta loads id -> (kind, name, project) for every active memory, so
// the report can name and classify injected ids without a per-row query.
func activeMemoryMeta(ctx context.Context, db *sql.DB) (map[string]memMeta, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, kind, name, project FROM memories_index WHERE invalid_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("store.activeMemoryMeta: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]memMeta{}
	for rows.Next() {
		var id, kind, name, project string
		if err := rows.Scan(&id, &kind, &name, &project); err != nil {
			return nil, fmt.Errorf("store.activeMemoryMeta: scan: %w", err)
		}
		out[id] = memMeta{kind: kind, name: name, project: project}
	}
	return out, rows.Err()
}
