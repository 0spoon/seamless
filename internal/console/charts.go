package console

import (
	"encoding/json"
	"fmt"
	"html/template"
	"sort"
	"strings"

	"github.com/0spoon/seamless/internal/store"
)

// anyCoverage reports whether any bucket in the window had at least one session.
func anyCoverage(buckets []store.CoverageBucket) bool {
	for _, b := range buckets {
		if b.Total > 0 {
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
	"layout-dashboard":      `<rect width="7" height="9" x="3" y="3" rx="1"/><rect width="7" height="5" x="14" y="3" rx="1"/><rect width="7" height="9" x="14" y="12" rx="1"/><rect width="7" height="5" x="3" y="16" rx="1"/>`,
	"terminal":              `<polyline points="4 17 10 11 4 5"/><line x1="12" x2="20" y1="19" y2="19"/>`,
	"activity":              `<polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/>`,
	"database":              `<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5V19A9 3 0 0 0 21 19V5"/><path d="M3 12A9 3 0 0 0 21 12"/>`,
	"arrow-down-to-line":    `<path d="M12 17V3"/><path d="m6 11 6 6 6-6"/><path d="M19 21H5"/>`,
	"list-checks":           `<path d="m3 17 2 2 4-4"/><path d="m3 7 2 2 4-4"/><path d="M13 6h8"/><path d="M13 12h8"/><path d="M13 18h8"/>`,
	"file-text":             `<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="M10 9H8"/><path d="M16 13H8"/><path d="M16 17H8"/>`,
	"sprout":                `<path d="M7 20h10"/><path d="M10 20c5.5-2.5.8-6.4 3-10"/><path d="M9.5 9.4c1.1.8 1.8 2.2 2.3 3.7-2 .4-3.5.4-4.8-.3-1.2-.6-2.3-1.9-3-4.2 2.8-.5 4.4 0 5.5.8z"/><path d="M14.1 6a7 7 0 0 0-1.1 4c1.9-.1 3.3-.6 4.3-1.4 1-1 1.6-2.3 1.7-4.6-2.7.1-4 1-4.9 2z"/>`,
	"settings":              `<path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/>`,
	"sun":                   `<circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/>`,
	"moon":                  `<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/>`,
	"log-out":               `<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" x2="9" y1="12" y2="12"/>`,
	"map":                   `<path d="M14.106 5.553a2 2 0 0 0 1.788 0l3.659-1.83A1 1 0 0 1 21 4.619v12.764a1 1 0 0 1-.553.894l-4.553 2.277a2 2 0 0 1-1.788 0l-4.212-2.106a2 2 0 0 0-1.788 0l-3.659 1.83A1 1 0 0 1 3 19.381V6.618a1 1 0 0 1 .553-.894l4.553-2.277a2 2 0 0 1 1.788 0z"/><path d="M15 5.764v15"/><path d="M9 3.236v15"/>`,
	"triangle-alert":        `<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z"/><path d="M12 9v4"/><path d="M12 17h.01"/>`,
	"copy":                  `<rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>`,
	"check":                 `<path d="M20 6 9 17l-5-5"/>`,
	"star":                  `<path d="M11.525 2.295a.53.53 0 0 1 .95 0l2.31 4.679a2.123 2.123 0 0 0 1.595 1.16l5.166.756a.53.53 0 0 1 .294.904l-3.736 3.638a2.123 2.123 0 0 0-.611 1.878l.882 5.14a.53.53 0 0 1-.771.56l-4.618-2.428a2.122 2.122 0 0 0-1.973 0L6.396 21.01a.53.53 0 0 1-.77-.56l.881-5.139a2.122 2.122 0 0 0-.611-1.879L2.16 9.795a.53.53 0 0 1 .294-.906l5.165-.755a2.122 2.122 0 0 0 1.597-1.16z"/>`,
	"search":                `<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>`,
	"table-2":               `<path d="M9 3H5a2 2 0 0 0-2 2v4m6-6h10a2 2 0 0 1 2 2v4M9 3v18m0 0h10a2 2 0 0 0 2-2V9M9 21H5a2 2 0 0 1-2-2V9m0 0h18"/>`,
	"server":                `<rect width="20" height="8" x="2" y="2" rx="2" ry="2"/><rect width="20" height="8" x="2" y="14" rx="2" ry="2"/><line x1="6" x2="6.01" y1="6" y2="6"/><line x1="6" x2="6.01" y1="18" y2="18"/>`,
	"folder-tree":           `<path d="M20 10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1h-2.5a1 1 0 0 1-.8-.4l-.9-1.2A1 1 0 0 0 15 3h-2a1 1 0 0 0-1 1v5a1 1 0 0 0 1 1Z"/><path d="M20 21a1 1 0 0 0 1-1v-3a1 1 0 0 0-1-1h-2.9a1 1 0 0 1-.88-.55l-.42-.85a1 1 0 0 0-.92-.6H13a1 1 0 0 0-1 1v5a1 1 0 0 0 1 1Z"/><path d="M3 5a2 2 0 0 0 2 2h3"/><path d="M3 3v13a2 2 0 0 0 2 2h3"/>`,
	"box":                   `<path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>`,
	"archive":               `<rect width="20" height="5" x="2" y="3" rx="1"/><path d="M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8"/><path d="M10 12h4"/>`,
	"git-fork":              `<circle cx="12" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><circle cx="18" cy="6" r="3"/><path d="M18 9v1a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2V9"/><path d="M12 12v3"/>`,
	"link":                  `<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>`,
	"git-branch":            `<line x1="6" x2="6" y1="3" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/>`,
	"git-merge":             `<circle cx="18" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><path d="M6 21V9a9 9 0 0 0 9 9"/>`,
	"git-commit-horizontal": `<circle cx="12" cy="12" r="3"/><line x1="3" x2="9" y1="12" y2="12"/><line x1="15" x2="21" y1="12" y2="12"/>`,
	"share-2":               `<circle cx="18" cy="5" r="3"/><circle cx="6" cy="12" r="3"/><circle cx="18" cy="19" r="3"/><line x1="8.59" x2="15.42" y1="13.51" y2="17.49"/><line x1="15.41" x2="8.59" y1="6.51" y2="10.49"/>`,
	"split":                 `<path d="M16 3h5v5"/><path d="M8 3H3v5"/><path d="M12 22v-8.3a4 4 0 0 0-1.172-2.872L3 3"/><path d="m15 9 6-6"/>`,
	"gauge":                 `<path d="m12 14 4-4"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/>`,
	"trending-up":           `<polyline points="22 7 13.5 15.5 8.5 10.5 2 17"/><polyline points="16 7 22 7 22 13"/>`,
	"brain":                 `<path d="M12 5a3 3 0 1 0-5.997.125 4 4 0 0 0-2.526 5.77 4 4 0 0 0 .556 6.588A4 4 0 1 0 12 18Z"/><path d="M12 5a3 3 0 1 1 5.997.125 4 4 0 0 1 2.526 5.77 4 4 0 0 1-.556 6.588A4 4 0 1 1 12 18Z"/><path d="M15 13a4.5 4.5 0 0 1-3-4 4.5 4.5 0 0 1-3 4"/>`,
	"circle":                `<circle cx="12" cy="12" r="10"/>`,
	"loader":                `<line x1="12" x2="12" y1="2" y2="6"/><line x1="12" x2="12" y1="18" y2="22"/><line x1="4.93" x2="7.76" y1="4.93" y2="7.76"/><line x1="16.24" x2="19.07" y1="16.24" y2="19.07"/><line x1="2" x2="6" y1="12" y2="12"/><line x1="18" x2="22" y1="12" y2="12"/><line x1="4.93" x2="7.76" y1="19.07" y2="16.24"/><line x1="16.24" x2="19.07" y1="7.76" y2="4.93"/>`,
	"lock":                  `<rect width="18" height="11" x="3" y="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>`,
	"timer":                 `<line x1="10" x2="14" y1="2" y2="2"/><line x1="12" x2="15" y1="14" y2="11"/><circle cx="12" cy="14" r="8"/>`,
	"arrow-right":           `<path d="M5 12h14"/><path d="m12 5 7 7-7 7"/>`,
	"folder-open":           `<path d="m6 14 1.5-2.9A2 2 0 0 1 9.24 10H20a2 2 0 0 1 1.94 2.5l-1.54 6a2 2 0 0 1-1.95 1.5H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H18a2 2 0 0 1 2 2v2"/>`,
	"flask-conical":         `<path d="M10 2v7.527a2 2 0 0 1-.211.896L4.72 20.55a1 1 0 0 0 .9 1.45h12.76a1 1 0 0 0 .9-1.45l-5.069-10.127A2 2 0 0 1 14 9.527V2"/><path d="M8.5 2h7"/><path d="M7 16h10"/>`,
	"test-tube":             `<path d="M14.5 2v17.5c0 1.4-1.1 2.5-2.5 2.5c-1.4 0-2.5-1.1-2.5-2.5V2"/><path d="M8.5 2h7"/><path d="M14.5 16h-5"/>`,
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
	case "gotcha", "constraint", "convention", "reference", "decision", "runbook", "protocol", "stage", "refuted":
		return "var(--k-" + kind + ")"
	default:
		return "var(--muted)"
	}
}

// ---------------------------------------------------------------------------
// Kind legend -- memories by kind
// ---------------------------------------------------------------------------

// kindLegend renders the memories-by-kind breakdown as labeled bars, largest
// kind first, widths normalized to the largest.
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
// Hover readout -- the payload static/charts.js snaps a crosshair to
// ---------------------------------------------------------------------------

// hoverPoint is one datum of a line chart's hover readout: where the crosshair
// snaps to (in viewBox units, so it needs no re-derivation client-side) and the
// already-formatted text the tooltip shows. The phrasing and the local-time
// labels are decided here for the same reason the rest of the chart is: the
// server is the only side that holds the window, the units, and the locale.
type hoverPoint struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Label string  `json:"label"`
	Value string  `json:"value"`
	Sub   string  `json:"sub,omitempty"`
}

// hoverData is a chart's whole hover payload: the viewBox (W x H) the client maps
// pointer pixels through, the Top/Bot the crosshair spans, and the points.
type hoverData struct {
	W   float64      `json:"w"`
	H   float64      `json:"h"`
	Top float64      `json:"top"`
	Bot float64      `json:"bot"`
	Pts []hoverPoint `json:"pts"`
}

// hoverAttr renders the data-hover attribute that arms a chart's hover readout.
// It is pure progressive enhancement -- charts.js is what reads this, and a chart
// still renders whole (with its aria-label, ticks and any <title> fallbacks)
// without it -- so nothing here may change the static drawing. Returns "" when the
// payload cannot be marshaled, dropping the readout rather than emitting a
// broken attribute.
func hoverAttr(w, h, top, bot float64, pts []hoverPoint) string {
	if len(pts) == 0 {
		return ""
	}
	b, err := json.Marshal(hoverData{W: w, H: h, Top: top, Bot: bot, Pts: pts})
	if err != nil {
		return ""
	}
	// json.Marshal already escapes <, > and &; HTMLEscapeString closes the quote
	// (a label is server-formatted, but this attribute is hand-written markup).
	return ` data-hover="` + template.HTMLEscapeString(string(b)) + `"`
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

	hov := make([]hoverPoint, 0, n)
	for i, p := range points {
		hov = append(hov, hoverPoint{
			X: x(i), Y: y(p.Count), Label: p.Label,
			Value: plural(p.Count, "injection", "injections"),
		})
	}

	alt := fmt.Sprintf("Injection trend across %d periods; peak %d at %s", n, points[peak].Count, points[peak].Label)
	var b strings.Builder
	fmt.Fprintf(&b, `<div class="area"%s>`, hoverAttr(w, h, padT, padT+ph, hov))
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" class="area-svg" style="color:var(--brand)" role="img" aria-label="%s" tabindex="0">`, w, h, template.HTMLEscapeString(alt))
	b.WriteString(`<defs><linearGradient id="areaGrad" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="currentColor" stop-opacity="0.20"/><stop offset="100%" stop-color="currentColor" stop-opacity="0.01"/></linearGradient></defs>`)
	// baseline gridlines
	for _, g := range []float64{0.25, 0.5, 0.75, 1} {
		yy := padT + ph*g
		fmt.Fprintf(&b, `<line x1="%g" x2="%g" y1="%.1f" y2="%.1f" stroke="var(--border)" stroke-width="1" stroke-dasharray="2 5" vector-effect="non-scaling-stroke"/>`, padL, padL+pw, yy, yy)
	}
	fmt.Fprintf(&b, `<path class="area-fill" d="%s" fill="url(#areaGrad)"/>`, area)
	// No vector-effect here, unlike the gridlines/peak: non-scaling-stroke moves the
	// dash pattern into screen space, which defeats the pathLength="1" normalization
	// the .area-line draw-on animation rides on. The dash then covers 1/scale of the
	// path, silently clipping the line's tail at any width past the 560-unit viewBox.
	fmt.Fprintf(&b, `<path class="area-line" d="%s" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linejoin="round" stroke-linecap="round" pathLength="1"/>`, line.String())
	if n == 1 {
		// A single point has no line/area to draw, and "peak" is meaningless, so
		// render the datum itself as a visible dot in the series color.
		fmt.Fprintf(&b, `<circle class="area-peak" cx="%.1f" cy="%.1f" r="4" fill="currentColor" stroke="var(--surface)" stroke-width="2.5" vector-effect="non-scaling-stroke"/>`, x(0), y(points[0].Count))
	} else {
		// peak marker
		fmt.Fprintf(&b, `<g class="area-peak"><line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="var(--pop)" stroke-width="1.5" stroke-dasharray="2 3" vector-effect="non-scaling-stroke"/><circle cx="%.1f" cy="%.1f" r="4" fill="var(--pop)" stroke="var(--surface)" stroke-width="2.5" vector-effect="non-scaling-stroke"/></g>`,
			x(peak), x(peak), y(points[peak].Count), padT+ph, x(peak), y(points[peak].Count))
	}
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

// ---------------------------------------------------------------------------
// Coverage trend -- retained-knowledge rate over time
// ---------------------------------------------------------------------------

// coverageTrend renders the windowed session-coverage rate as a line + area fill
// on a fixed 0-100% axis, in the coverage green. The rate is covered/total per
// bucket; buckets with no sessions are backfilled to 0% so a quiet stretch reads
// as a dip in retention rather than an implied ceiling, and only buckets that
// actually had sessions are marked with a dot. It expects a dense bucket series
// (SessionCoverageBuckets); when no bucket in the window had a session it renders
// nothing so the caller can fall back to an empty state.
func coverageTrend(buckets []store.CoverageBucket) template.HTML {
	n := len(buckets)
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

	// Coverage rate per bucket; buckets with no sessions count as 0% (backfilled)
	// so the line spans the whole window and quiet buckets visibly drop to the
	// floor.
	pctOf := func(b store.CoverageBucket) float64 {
		if b.Total == 0 {
			return 0
		}
		return float64(b.Covered) / float64(b.Total) * 100
	}
	if !anyCoverage(buckets) {
		return "" // no sessions anywhere in the window
	}

	var line, area, dots strings.Builder
	hov := make([]hoverPoint, 0, n)
	for i, d := range buckets {
		cmd := "L"
		if i == 0 {
			cmd = "M"
		}
		fmt.Fprintf(&line, "%s%.1f %.1f ", cmd, x(i), y(pctOf(d)))
		if d.Total > 0 { // mark only buckets that actually had sessions
			// The <title> is the no-JS readout: it survives as the fallback for the
			// charts.js hover layer, which masks it with its own capture rect (two
			// tooltips for one point would otherwise stack up).
			fmt.Fprintf(&dots, `<circle class="cov-dot" cx="%.1f" cy="%.1f" r="3.5" fill="var(--ok)" stroke="var(--surface)" stroke-width="2" vector-effect="non-scaling-stroke"><title>%s: %d%% (%d of %d)</title></circle>`,
				x(i), y(pctOf(d)), template.HTMLEscapeString(d.Label), int(pctOf(d)+0.5), d.Covered, d.Total)
		}
		p := hoverPoint{X: x(i), Y: y(pctOf(d)), Label: d.Label}
		// A quiet bucket is drawn at the floor but is NOT 0% retention; say so
		// rather than let the reader take the dip at face value.
		if d.Total == 0 {
			p.Value = "No sessions"
		} else {
			p.Value = fmt.Sprintf("%d%% covered", int(pctOf(d)+0.5))
			p.Sub = fmt.Sprintf("%d of %s retained knowledge", d.Covered, plural(d.Total, "session", "sessions"))
		}
		hov = append(hov, p)
	}
	// One continuous area under the line, pinching to the baseline on empty buckets.
	fmt.Fprintf(&area, "M%.1f %.1f ", x(0), padT+ph)
	for i, d := range buckets {
		fmt.Fprintf(&area, "L%.1f %.1f ", x(i), y(pctOf(d)))
	}
	fmt.Fprintf(&area, "L%.1f %.1f Z", x(n-1), padT+ph)

	var cov, tot int
	for _, d := range buckets {
		cov += d.Covered
		tot += d.Total
	}
	rate := 0
	if tot > 0 {
		rate = int(float64(cov)/float64(tot)*100 + 0.5)
	}
	alt := fmt.Sprintf("Session coverage trend across %d periods; %d%% of %d sessions left a durable artifact", n, rate, tot)
	var b strings.Builder
	fmt.Fprintf(&b, `<div class="area"%s>`, hoverAttr(w, h, padT, padT+ph, hov))
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" class="area-svg" style="color:var(--ok)" role="img" aria-label="%s" tabindex="0">`, w, h, template.HTMLEscapeString(alt))
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
	// axis ticks: first / middle / last of the window
	b.WriteString(`<div class="area-ticks">`)
	ticks := []int{0}
	if n > 2 {
		ticks = append(ticks, n/2)
	}
	if n > 1 {
		ticks = append(ticks, n-1)
	}
	for _, i := range ticks {
		fmt.Fprintf(&b, `<span>%s</span>`, template.HTMLEscapeString(buckets[i].Label))
	}
	b.WriteString(`</div></div>`)
	return template.HTML(b.String())
}

// ---------------------------------------------------------------------------
// Bar chart -- events by kind (session detail)
// ---------------------------------------------------------------------------

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
		// A neutral mid-grey, not --surface-3: the latter is ~= the track fill
		// (--surface-2), which made a Closed segment vanish into the empty track.
		{"Closed", closed, "var(--muted)"},
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
