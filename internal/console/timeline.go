package console

// The proportional session timeline. The session page previously drew its
// activity as uniform time slices, which gives a 5-second injection and a
// ten-minute tool burst the same visual weight -- exactly backwards for the
// question the strip is there to answer ("where did this session actually spend
// its time"). Here a block's width IS its wall clock.

import (
	"fmt"
	"html/template"
	"strings"
	"time"
)

const (
	// ptlMergeGap is how close two same-class events have to be to read as one
	// stretch of work rather than two. Below it, a burst of tool calls is one
	// block labeled with its count; above it, the quiet between them is real and
	// shows as idle.
	ptlMergeGap = 30 * time.Second
	// ptlLabelPct is the share of the track a block needs before its label fits.
	// Narrower blocks keep the label in their tooltip instead of clipping it.
	ptlLabelPct = 6.0
)

// ptlBlock is one block of the timeline: a class that colors it, a width that
// is its share of the session's wall clock, and the first event it covers so a
// click can take the reader to that point in the trace.
type ptlBlock struct {
	Class    string  // tool|inject|error|write|idle
	Pct      float64 // share of the session's span
	Label    string  // rendered only when the block is wide enough to hold it
	Title    string  // native tooltip: what, how many, when
	AnchorID string  // first event id in the block; empty for idle
}

// sessionTimeline is the whole strip: its blocks, the scale ticks under them,
// and whether there was enough of a span to draw anything at all.
type sessionTimeline struct {
	Blocks []ptlBlock
	Scale  []string
	Show   bool
}

// ptlClass maps an event to the timeline's visual vocabulary. It is coarser than
// evtSev on purpose: this strip answers "what kind of work was happening", so
// tool traffic gets its own class that the severity code (which cares about
// urgency) folds into "system".
func ptlClass(kind string, isError bool) string {
	if isError {
		return "error"
	}
	switch kind {
	case "retrieval.injected":
		return "inject"
	case "memory.written", "note.written", "trial.recorded":
		return "write"
	default:
		return "tool"
	}
}

// ptlLabel names a merged run for its in-block label ("tools x14").
func ptlLabel(class string, n int) string {
	noun := map[string]string{
		"tool": "calls", "inject": "injects", "error": "errors", "write": "writes",
	}[class]
	if noun == "" {
		return ""
	}
	if n == 1 {
		return strings.TrimSuffix(noun, "s")
	}
	return fmt.Sprintf("%s &times;%d", noun, n)
}

// buildSessionTimeline lays the session's interactions out on wall clock. rows
// arrive newest-first (the feed's order); the strip reads left to right in time,
// so it walks them backwards.
//
// A session whose events all land in the same instant has no span to divide, so
// the strip does not render: a set of equal blocks would be the uniform strip
// this replaces, dressed up as a measurement.
func buildSessionTimeline(rows []interactionRow) sessionTimeline {
	if len(rows) < 2 {
		return sessionTimeline{}
	}
	ordered := make([]interactionRow, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		ordered = append(ordered, rows[i])
	}
	start, end := ordered[0].TS, ordered[len(ordered)-1].TS
	span := end.Sub(start)
	if span <= 0 {
		return sessionTimeline{}
	}

	// A run is a stretch of same-class events no more than ptlMergeGap apart.
	type run struct {
		class      string
		from, to   time.Time
		n          int
		anchor     string
		firstLabel string
	}
	var runs []run
	for _, r := range ordered {
		class := ptlClass(r.Kind, r.IsError)
		if n := len(runs); n > 0 {
			last := &runs[n-1]
			if last.class == class && r.TS.Sub(last.to) <= ptlMergeGap {
				last.to = r.TS
				last.n++
				continue
			}
		}
		runs = append(runs, run{class: class, from: r.TS, to: r.TS, n: 1, anchor: r.ID, firstLabel: r.Label})
	}

	pct := func(d time.Duration) float64 { return float64(d) / float64(span) * 100 }
	out := make([]ptlBlock, 0, len(runs)*2)
	for i, rn := range runs {
		if i > 0 {
			// The quiet between two runs is the model thinking, or the user away.
			// Either way it is time the session spent, so it takes up room.
			if gap := rn.from.Sub(runs[i-1].to); gap > ptlMergeGap {
				out = append(out, ptlBlock{
					Class: "idle", Pct: pct(gap),
					Title: fmt.Sprintf("Idle %s - %s - %s", fmtOffset(runs[i-1].to.Sub(start)), fmtOffset(rn.from.Sub(start)), compactDur(gap)),
				})
			}
		}
		b := ptlBlock{Class: rn.class, Pct: pct(rn.to.Sub(rn.from)), AnchorID: rn.anchor}
		label := ptlLabel(rn.class, rn.n)
		if b.Pct >= ptlLabelPct {
			b.Label = label
		}
		what := rn.firstLabel
		if what == "" {
			what = rn.class
		}
		if rn.from.Equal(rn.to) {
			b.Title = fmt.Sprintf("%s - %d event - %s", what, rn.n, fmtOffset(rn.from.Sub(start)))
			if rn.n != 1 {
				b.Title = fmt.Sprintf("%s - %d events - %s", what, rn.n, fmtOffset(rn.from.Sub(start)))
			}
		} else {
			b.Title = fmt.Sprintf("%s - %d events - %s to %s", what, rn.n,
				fmtOffset(rn.from.Sub(start)), fmtOffset(rn.to.Sub(start)))
		}
		out = append(out, b)
	}

	return sessionTimeline{Blocks: out, Scale: timelineScale(span), Show: true}
}

// timelineScale renders the ticks under the track: start, two interior marks,
// and the total.
func timelineScale(span time.Duration) []string {
	return []string{
		fmtOffset(0),
		fmtOffset(span / 3),
		fmtOffset(span * 2 / 3),
		fmtOffset(span),
	}
}

// fmtOffset renders an offset from the session's start as m:ss, or h:mm:ss once
// a session runs past an hour.
func fmtOffset(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Round(time.Second).Seconds())
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// compactDur renders a gap for a tooltip ("4m", "1h 12m", "18s").
func compactDur(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// ptlTrack renders the timeline blocks. Each block is an anchor so it is
// keyboard-reachable without extra tabindex wiring; the idle stretches are inert
// spans, because there is no event under them to navigate to.
func ptlTrack(t sessionTimeline) template.HTML {
	if !t.Show || len(t.Blocks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class="ptl-track">`)
	for _, blk := range t.Blocks {
		style := fmt.Sprintf(`style="width:%.3f%%"`, blk.Pct)
		title := template.HTMLEscapeString(blk.Title)
		if blk.AnchorID == "" {
			fmt.Fprintf(&b, `<span data-class="%s" %s title="%s"></span>`, blk.Class, style, title)
			continue
		}
		fmt.Fprintf(&b, `<a href="#%s" data-class="%s" %s title="%s" data-ptl-anchor="%s">%s</a>`,
			template.HTMLEscapeString(blk.AnchorID), blk.Class, style, title,
			template.HTMLEscapeString(blk.AnchorID), blk.Label)
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}
