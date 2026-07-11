package console

import (
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/store"
)

// denseDays expands a sparse day series (store.InjectionsByDay omits empty days)
// into a dense trailing-window of exactly n daily buckets ending today (UTC),
// filling gaps with zero. A continuous series makes the area chart read as a
// real trend rather than a lone point.
func denseDays(sparse []store.DayCount, n int) []store.DayCount {
	if n <= 0 {
		return nil
	}
	byDay := make(map[string]int, len(sparse))
	for _, d := range sparse {
		byDay[d.Day] = d.Count
	}
	today := time.Now().UTC()
	out := make([]store.DayCount, 0, n)
	for i := n - 1; i >= 0; i-- {
		day := today.AddDate(0, 0, -i).Format("2006-01-02")
		out = append(out, store.DayCount{Day: day, Count: byDay[day]})
	}
	return out
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
	"database":           `<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5V19A9 3 0 0 0 21 19V5"/><path d="M3 12A9 3 0 0 0 21 12"/>`,
	"arrow-down-to-line": `<path d="M12 17V3"/><path d="m6 11 6 6 6-6"/><path d="M19 21H5"/>`,
	"list-checks":        `<path d="m3 17 2 2 4-4"/><path d="m3 7 2 2 4-4"/><path d="M13 6h8"/><path d="M13 12h8"/><path d="M13 18h8"/>`,
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
// Donut -- memories by kind
// ---------------------------------------------------------------------------

// donut renders a ring (one arc per kind) with a center total and a legend,
// matching the design brief's memories-by-kind visual. Empty input renders
// nothing so callers can fall back to an empty state.
func donut(items []kindCount) template.HTML {
	total := 0
	for _, it := range items {
		total += it.N
	}
	if total == 0 {
		return ""
	}
	const size, stroke = 128.0, 20.0
	r := (size - stroke) / 2
	circ := 2 * math.Pi * r

	var b strings.Builder
	b.WriteString(`<div class="donut"><div class="donut-ring">`)
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" class="donut-svg" width="%g" height="%g"><g transform="rotate(-90 %g %g)">`, size, size, size, size, size/2, size/2)
	fmt.Fprintf(&b, `<circle cx="%g" cy="%g" r="%g" fill="none" stroke="var(--surface-2)" stroke-width="%g"/>`, size/2, size/2, r, stroke)
	acc := 0.0
	for _, it := range items {
		frac := float64(it.N) / float64(total)
		dash := frac * circ
		fmt.Fprintf(&b, `<circle class="donut-seg" cx="%g" cy="%g" r="%g" fill="none" stroke-width="%g" stroke-linecap="butt" style="stroke:%s;stroke-dasharray:%.2f %.2f;stroke-dashoffset:%.2f"/>`,
			size/2, size/2, r, stroke, kindColorVar(it.Kind), dash, circ-dash, -acc*circ)
		acc += frac
	}
	b.WriteString(`</g></svg>`)
	fmt.Fprintf(&b, `<div class="donut-center"><span class="donut-total">%d</span><span class="donut-cap">total</span></div>`, total)
	b.WriteString(`</div><ul class="donut-legend">`)
	for _, it := range items {
		fmt.Fprintf(&b, `<li><span class="kdot" style="background:%s"></span><span class="donut-k">%s</span><span class="donut-n">%d</span></li>`,
			kindColorVar(it.Kind), template.HTMLEscapeString(it.Kind), it.N)
	}
	b.WriteString(`</ul></div>`)
	return template.HTML(b.String())
}

// ---------------------------------------------------------------------------
// Area chart -- injection trend
// ---------------------------------------------------------------------------

// areaChart renders an injection trend as an SVG line + gradient fill with the
// peak day flagged in coral, and up to three date ticks below. Points are
// plotted by index (sparse days are fine). Empty input renders nothing.
func areaChart(days []store.DayCount) template.HTML {
	n := len(days)
	if n == 0 {
		return ""
	}
	const w, h = 560.0, 150.0
	const padT, padR, padB, padL = 14.0, 12.0, 8.0, 12.0
	pw := w - padL - padR
	ph := h - padT - padB

	maxV, peak := 1, 0
	for i, d := range days {
		if d.Count > maxV {
			maxV = d.Count
		}
		if d.Count > days[peak].Count {
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
	for i, d := range days {
		cmd := "L"
		if i == 0 {
			cmd = "M"
		}
		fmt.Fprintf(&line, "%s%.1f %.1f ", cmd, x(i), y(d.Count))
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
		x(peak), x(peak), y(days[peak].Count), padT+ph, x(peak), y(days[peak].Count))
	b.WriteString(`</svg>`)
	// date ticks: first / middle / last
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
