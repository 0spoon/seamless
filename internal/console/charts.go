package console

import (
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/store"
)

// denseCoverageDays expands a sparse per-day coverage series (SessionCoverageByDay
// omits days with no sessions) into a dense trailing-window of exactly n daily
// buckets ending today (viewer-local, matching the store's local-day keys).
// Missing days become zero-Total buckets, which the trend chart renders as gaps
// (no sessions -> coverage undefined, not 0%).
func denseCoverageDays(sparse []store.DayCoverage, n int) []store.DayCoverage {
	if n <= 0 {
		return nil
	}
	byDay := make(map[string]store.DayCoverage, len(sparse))
	for _, d := range sparse {
		byDay[d.Day] = d
	}
	today := time.Now().Local()
	out := make([]store.DayCoverage, 0, n)
	for i := n - 1; i >= 0; i-- {
		day := today.AddDate(0, 0, -i).Format("2006-01-02")
		if d, ok := byDay[day]; ok {
			out = append(out, d)
		} else {
			out = append(out, store.DayCoverage{Day: day})
		}
	}
	return out
}

// coverageTrendData densifies a sparse per-day coverage series into the trend
// chart's n-day window, returning nil when the window holds no sessions at all
// so the overview can show an empty state instead of a flat, dataless chart.
func coverageTrendData(sparse []store.DayCoverage, n int) []store.DayCoverage {
	dense := denseCoverageDays(sparse, n)
	if !anySessions(dense) {
		return nil
	}
	return dense
}

// anySessions reports whether any day in the window had at least one session.
func anySessions(days []store.DayCoverage) bool {
	for _, d := range days {
		if d.Total > 0 {
			return true
		}
	}
	return false
}

// This file holds the console's self-contained visual primitives: inline Lucide
// icons and hand-rolled SVG/flex charts. They render server-side to
// template.HTML (no charting library, no client JS), matching the "Seamless
// Console Design Brief" reference UI kit. Every value fed in is
// server-controlled (counts, enum kinds); free-form strings are escaped.

// ---------------------------------------------------------------------------
// Icons -- a small inline Lucide set (1.75px stroke, rounded, geometric).
// ---------------------------------------------------------------------------

// lucidePaths maps an icon name to the inner markup of a 24x24 Lucide glyph.
var lucidePaths = map[string]string{
	"layout-dashboard":   `<rect width="7" height="9" x="3" y="3" rx="1"/><rect width="7" height="5" x="14" y="3" rx="1"/><rect width="7" height="9" x="14" y="12" rx="1"/><rect width="7" height="5" x="3" y="16" rx="1"/>`,
	"terminal":           `<polyline points="4 17 10 11 4 5"/><line x1="12" x2="20" y1="19" y2="19"/>`,
	"activity":           `<polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/>`,
	"database":           `<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5V19A9 3 0 0 0 21 19V5"/><path d="M3 12A9 3 0 0 0 21 12"/>`,
	"arrow-down-to-line": `<path d="M12 17V3"/><path d="m6 11 6 6 6-6"/><path d="M19 21H5"/>`,
	"list-checks":        `<path d="m3 17 2 2 4-4"/><path d="m3 7 2 2 4-4"/><path d="M13 6h8"/><path d="M13 12h8"/><path d="M13 18h8"/>`,
	"file-text":          `<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="M10 9H8"/><path d="M16 13H8"/><path d="M16 17H8"/>`,
	"sprout":             `<path d="M7 20h10"/><path d="M10 20c5.5-2.5.8-6.4 3-10"/><path d="M9.5 9.4c1.1.8 1.8 2.2 2.3 3.7-2 .4-3.5.4-4.8-.3-1.2-.6-2.3-1.9-3-4.2 2.8-.5 4.4 0 5.5.8z"/><path d="M14.1 6a7 7 0 0 0-1.1 4c1.9-.1 3.3-.6 4.3-1.4 1-1 1.6-2.3 1.7-4.6-2.7.1-4 1-4.9 2z"/>`,
	"settings":           `<path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/>`,
	"sun":                `<circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/>`,
	"moon":               `<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/>`,
	"log-out":            `<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" x2="9" y1="12" y2="12"/>`,
	"triangle-alert":     `<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z"/><path d="M12 9v4"/><path d="M12 17h.01"/>`,
}

// icon renders a named Lucide glyph as an inline SVG that inherits currentColor.
// Unknown names render nothing so a typo can't break a page.
func icon(name string) template.HTML {
	p, ok := lucidePaths[name]
	if !ok {
		return ""
	}
	return template.HTML(`<svg class="ico ico-` + name + `" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` + p + `</svg>`)
}

// ---------------------------------------------------------------------------
// Color mappings
// ---------------------------------------------------------------------------

// kindColorVar returns the CSS color for a memory kind (the same per-kind hues
// the legend/chart dots use). Unknown kinds fall back to the muted tone.
func kindColorVar(kind string) string {
	switch kind {
	case "gotcha", "constraint", "reference", "decision", "runbook", "protocol", "stage", "refuted":
		return "var(--k-" + kind + ")"
	default:
		return "var(--muted)"
	}
}

// evtColorVar maps an event kind to a chart color, reusing evtTone so bars and
// their matching chips read in the same palette.
func evtColorVar(kind string) string {
	switch evtTone(kind) {
	case "brand":
		return "var(--brand)"
	case "ok":
		return "var(--ok)"
	case "pop":
		return "var(--pop)"
	case "warn":
		return "var(--warn)"
	default:
		return "var(--muted)"
	}
}

// ---------------------------------------------------------------------------
// Kind legend -- memories by kind (rail variant of the donut)
// ---------------------------------------------------------------------------

// kindLegend renders the memories-by-kind breakdown as labeled bars (the rail
// variant of the donut), largest kind first, widths normalized to the largest.
func kindLegend(items []kindCount) template.HTML {
	if len(items) == 0 {
		return ""
	}
	maxV := 1
	for _, it := range items {
		if it.N > maxV {
			maxV = it.N
		}
	}
	sorted := make([]kindCount, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].N > sorted[j].N })
	var b strings.Builder
	b.WriteString(`<div class="legend">`)
	for _, it := range sorted {
		c := kindColorVar(it.Kind)
		fmt.Fprintf(&b, `<div class="legend-row"><span class="kdot" style="background:%s"></span><span class="legend-label">%s</span><span class="legend-bar"><span style="width:%d%%;background:%s"></span></span><span class="legend-val">%d</span></div>`,
			c, template.HTMLEscapeString(it.Kind), percent(it.N, maxV), c, it.N)
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

// kindBars renders per-kind injection volume for the Retrieval page: one bar per
// memory kind (width normalized to the busiest kind), annotated with the raw
// injection count and how many distinct memories of that kind were surfaced.
func kindBars(rows []store.KindReach) template.HTML {
	if len(rows) == 0 {
		return ""
	}
	maxV := 1
	for _, r := range rows {
		if r.Injects > maxV {
			maxV = r.Injects
		}
	}
	var b strings.Builder
	b.WriteString(`<div class="kindbars">`)
	for _, r := range rows {
		fmt.Fprintf(&b, `<div class="kindbar"><span class="kind">%s</span><div class="bars"><div class="track"><span class="inj" style="width:%d%%"></span></div></div><span class="nums">%d &middot; %d mem</span></div>`,
			template.HTMLEscapeString(r.Kind), percent(r.Injects, maxV), r.Injects, r.Memories)
	}
	b.WriteString(`</div><div class="kindbar-legend"><span><i style="background:var(--brand)"></i> injections</span><span class="kb-note">&middot; distinct memories surfaced</span></div>`)
	return template.HTML(b.String())
}

// ---------------------------------------------------------------------------
// Area chart -- injection trend
// ---------------------------------------------------------------------------

// areaChart renders an injection trend as an SVG line + gradient fill with the
// peak bucket flagged in coral, and up to three ticks below. Points are plotted
// by index; each carries its own pre-formatted (local-time) tick label. Empty
// input renders nothing.
func areaChart(points []store.TrendBucket) template.HTML {
	n := len(points)
	if n == 0 {
		return ""
	}
	const w, h = 560.0, 150.0
	const padT, padR, padB, padL = 14.0, 12.0, 8.0, 12.0
	pw := w - padL - padR
	ph := h - padT - padB

	maxV, peak := 1, 0
	for i, p := range points {
		if p.Count > maxV {
			maxV = p.Count
		}
		if p.Count > points[peak].Count {
			peak = i
		}
	}
	x := func(i int) float64 {
		if n == 1 {
			return padL + pw/2
		}
		return padL + (float64(i)/float64(n-1))*pw
	}
	y := func(v int) float64 { return padT + ph - (float64(v)/float64(maxV))*ph }

	var line strings.Builder
	for i, p := range points {
		cmd := "L"
		if i == 0 {
			cmd = "M"
		}
		fmt.Fprintf(&line, "%s%.1f %.1f ", cmd, x(i), y(p.Count))
	}
	area := line.String() + fmt.Sprintf("L%.1f %.1f L%.1f %.1f Z", x(n-1), padT+ph, x(0), padT+ph)

	var b strings.Builder
	b.WriteString(`<div class="area">`)
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" width="100%%" height="auto" class="area-svg" style="color:var(--brand)">`, w, h)
	b.WriteString(`<defs><linearGradient id="areaGrad" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="currentColor" stop-opacity="0.20"/><stop offset="100%" stop-color="currentColor" stop-opacity="0.01"/></linearGradient></defs>`)
	// baseline gridlines
	for _, g := range []float64{0.25, 0.5, 0.75, 1} {
		yy := padT + ph*g
		fmt.Fprintf(&b, `<line x1="%g" x2="%g" y1="%.1f" y2="%.1f" stroke="var(--border)" stroke-width="1" stroke-dasharray="2 5" vector-effect="non-scaling-stroke"/>`, padL, padL+pw, yy, yy)
	}
	fmt.Fprintf(&b, `<path class="area-fill" d="%s" fill="url(#areaGrad)"/>`, area)
	fmt.Fprintf(&b, `<path class="area-line" d="%s" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linejoin="round" stroke-linecap="round" pathLength="1" vector-effect="non-scaling-stroke"/>`, line.String())
	// peak marker
	fmt.Fprintf(&b, `<g class="area-peak"><line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="var(--pop)" stroke-width="1.5" stroke-dasharray="2 3" vector-effect="non-scaling-stroke"/><circle cx="%.1f" cy="%.1f" r="4" fill="var(--pop)" stroke="var(--surface)" stroke-width="2.5" vector-effect="non-scaling-stroke"/></g>`,
		x(peak), x(peak), y(points[peak].Count), padT+ph, x(peak), y(points[peak].Count))
	b.WriteString(`</svg>`)
	// ticks: first / middle / last, using each bucket's own label
	b.WriteString(`<div class="area-ticks">`)
	ticks := []int{0}
	if n > 2 {
		ticks = append(ticks, n/2)
	}
	if n > 1 {
		ticks = append(ticks, n-1)
	}
	for _, i := range ticks {
		fmt.Fprintf(&b, `<span>%s</span>`, template.HTMLEscapeString(points[i].Label))
	}
	b.WriteString(`</div></div>`)
	return template.HTML(b.String())
}

// fmtDay renders a YYYY-MM-DD key as a compact "Jan 02" tick, falling back to
// the raw string if it does not parse.
func fmtDay(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.Format("Jan 02")
}

// ---------------------------------------------------------------------------
// Coverage trend -- retained-knowledge rate over time
// ---------------------------------------------------------------------------

// coverageTrend renders the trailing-window session-coverage rate as a line +
// area fill on a fixed 0-100% axis, in the coverage green. The rate is
// covered/total per day; days with no sessions are backfilled to 0% so a quiet
// stretch reads as a dip in retention rather than an implied ceiling, and only
// days that actually had sessions are marked with a dot. It expects a dense day
// series (denseCoverageDays); when no day in the window had a session it renders
// nothing so the caller can fall back to an empty state.
func coverageTrend(days []store.DayCoverage) template.HTML {
	n := len(days)
	if n == 0 {
		return ""
	}
	const w, h = 560.0, 150.0
	const padT, padR, padB, padL = 14.0, 12.0, 8.0, 30.0
	pw := w - padL - padR
	ph := h - padT - padB

	x := func(i int) float64 {
		if n == 1 {
			return padL + pw/2
		}
		return padL + (float64(i)/float64(n-1))*pw
	}
	y := func(pct float64) float64 { return padT + ph - (pct/100)*ph }

	// Coverage rate per day; days with no sessions count as 0% (backfilled) so
	// the line spans the whole window and quiet days visibly drop to the floor.
	pctOf := func(d store.DayCoverage) float64 {
		if d.Total == 0 {
			return 0
		}
		return float64(d.Covered) / float64(d.Total) * 100
	}
	if !anySessions(days) {
		return "" // no sessions anywhere in the window
	}

	var line, area, dots strings.Builder
	for i, d := range days {
		cmd := "L"
		if i == 0 {
			cmd = "M"
		}
		fmt.Fprintf(&line, "%s%.1f %.1f ", cmd, x(i), y(pctOf(d)))
		if d.Total > 0 { // mark only days that actually had sessions
			fmt.Fprintf(&dots, `<circle class="cov-dot" cx="%.1f" cy="%.1f" r="3.5" fill="var(--ok)" stroke="var(--surface)" stroke-width="2" vector-effect="non-scaling-stroke"><title>%s: %d%% (%d of %d)</title></circle>`,
				x(i), y(pctOf(d)), template.HTMLEscapeString(fmtDay(d.Day)), int(pctOf(d)+0.5), d.Covered, d.Total)
		}
	}
	// One continuous area under the line, pinching to the baseline on empty days.
	fmt.Fprintf(&area, "M%.1f %.1f ", x(0), padT+ph)
	for i, d := range days {
		fmt.Fprintf(&area, "L%.1f %.1f ", x(i), y(pctOf(d)))
	}
	fmt.Fprintf(&area, "L%.1f %.1f Z", x(n-1), padT+ph)

	var b strings.Builder
	b.WriteString(`<div class="area">`)
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" width="100%%" height="auto" class="area-svg" style="color:var(--ok)">`, w, h)
	b.WriteString(`<defs><linearGradient id="covGrad" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="currentColor" stop-opacity="0.20"/><stop offset="100%" stop-color="currentColor" stop-opacity="0.01"/></linearGradient></defs>`)
	// horizontal gridlines with % axis labels at 100 / 50 / 0
	for _, g := range []struct {
		frac  float64
		label string
	}{{0, "100%"}, {0.25, ""}, {0.5, "50%"}, {0.75, ""}, {1, "0%"}} {
		yy := padT + ph*g.frac
		fmt.Fprintf(&b, `<line x1="%g" x2="%g" y1="%.1f" y2="%.1f" stroke="var(--border)" stroke-width="1" stroke-dasharray="2 5" vector-effect="non-scaling-stroke"/>`, padL, padL+pw, yy, yy)
		if g.label != "" {
			fmt.Fprintf(&b, `<text class="cov-axis" x="%.1f" y="%.1f" text-anchor="end" dominant-baseline="middle">%s</text>`, padL-6, yy, g.label)
		}
	}
	fmt.Fprintf(&b, `<path class="cov-fill" d="%s" fill="url(#covGrad)"/>`, area.String())
	fmt.Fprintf(&b, `<path class="cov-line" d="%s" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linejoin="round" stroke-linecap="round"/>`, line.String())
	b.WriteString(dots.String())
	b.WriteString(`</svg>`)
	// date ticks: first / middle / last of the window
	b.WriteString(`<div class="area-ticks">`)
	ticks := []int{0}
	if n > 2 {
		ticks = append(ticks, n/2)
	}
	if n > 1 {
		ticks = append(ticks, n-1)
	}
	for _, i := range ticks {
		fmt.Fprintf(&b, `<span>%s</span>`, template.HTMLEscapeString(fmtDay(days[i].Day)))
	}
	b.WriteString(`</div></div>`)
	return template.HTML(b.String())
}

// ---------------------------------------------------------------------------
// Bar chart -- events by kind (session detail)
// ---------------------------------------------------------------------------

// barChart renders a small vertical bar per kind, value above and label below,
// colored to match the event chips. Empty input renders nothing.
func barChart(items []kindCount) template.HTML {
	if len(items) == 0 {
		return ""
	}
	maxV := 1
	for _, it := range items {
		if it.N > maxV {
			maxV = it.N
		}
	}
	var b strings.Builder
	b.WriteString(`<div class="barchart">`)
	for _, it := range items {
		label := it.Kind
		if i := strings.LastIndex(label, "."); i >= 0 && i < len(label)-1 {
			label = label[i+1:]
		}
		hp := float64(it.N) / float64(maxV) * 100
		fmt.Fprintf(&b, `<div class="barcol"><div class="bartrack"><span class="barval">%d</span><div class="bar" style="height:%.1f%%;background:%s"></div></div><span class="barlabel">%s</span></div>`,
			it.N, hp, evtColorVar(it.Kind), template.HTMLEscapeString(label))
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

// ---------------------------------------------------------------------------
// Stacked bar -- task pipeline
// ---------------------------------------------------------------------------

// stackedBar renders the task pipeline as one proportional bar plus a legend.
// Zero-width segments are dropped from the bar but kept in the legend so the
// four-count rhythm stays intact. All-zero renders nothing.
func stackedBar(ready, inProgress, blocked, closed int) template.HTML {
	segs := []struct {
		label string
		n     int
		color string
	}{
		{"Ready", ready, "var(--ok)"},
		{"In progress", inProgress, "var(--brand)"},
		{"Blocked", blocked, "var(--warn)"},
		{"Closed", closed, "var(--surface-3)"},
	}
	total := ready + inProgress + blocked + closed
	if total == 0 {
		return ""
	}
	var bar, legend strings.Builder
	for _, s := range segs {
		if s.n > 0 {
			fmt.Fprintf(&bar, `<div class="stackseg" style="width:%.2f%%;background:%s" title="%s %d"></div>`,
				float64(s.n)/float64(total)*100, s.color, s.label, s.n)
		}
		fmt.Fprintf(&legend, `<div class="stackleg"><span class="kdot" style="background:%s"></span><span class="stackleg-l">%s</span><span class="stackleg-n">%d</span></div>`,
			s.color, s.label, s.n)
	}
	return template.HTML(`<div class="stackbar-wrap"><div class="stackbar">` + bar.String() + `</div><div class="stackbar-legend">` + legend.String() + `</div></div>`)
}
