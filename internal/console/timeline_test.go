package console

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// rowsAt builds newest-first interaction rows (the order the session handler
// hands the builder) from oldest-first specs.
func rowsAt(base time.Time, specs ...struct {
	off   time.Duration
	kind  string
	isErr bool
},
) []interactionRow {
	out := make([]interactionRow, 0, len(specs))
	for i := len(specs) - 1; i >= 0; i-- {
		s := specs[i]
		out = append(out, interactionRow{
			ID: string(rune('a' + i)), TS: base.Add(s.off), Kind: s.kind, IsError: s.isErr,
		})
	}
	return out
}

type spec = struct {
	off   time.Duration
	kind  string
	isErr bool
}

func TestBuildSessionTimeline_WidthIsWallClock(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	// A ten-minute tool burst (a call every 15s, so it reads as one stretch of
	// work), then a five-second injection: the point is that the first block
	// dominates the strip the way it dominated the session.
	var specs []spec
	for d := time.Duration(0); d <= 10*time.Minute; d += 15 * time.Second {
		specs = append(specs, spec{off: d, kind: "tool.call"})
	}
	specs = append(specs, spec{off: 10*time.Minute + 5*time.Second, kind: "retrieval.injected"})
	strip := buildSessionTimeline(rowsAt(base, specs...))
	require.True(t, strip.Show)

	var tool, inject float64
	for _, b := range strip.Blocks {
		switch b.Class {
		case "tool":
			tool += b.Pct
		case "inject":
			inject += b.Pct
		}
	}
	require.Greater(t, tool, 90.0, "the burst owns nearly the whole span")
	require.Equal(t, 0.0, inject, "a trailing instant takes no wall clock; min-width keeps it clickable")
	require.Equal(t, "0:00", strip.Scale[0])
	require.Equal(t, "10:05", strip.Scale[len(strip.Scale)-1])
}

func TestBuildSessionTimeline_MergesBurstsAndKeepsQuietVisible(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	strip := buildSessionTimeline(rowsAt(base,
		spec{off: 0, kind: "tool.call"},
		spec{off: 10 * time.Second, kind: "tool.call"},
		spec{off: 25 * time.Second, kind: "tool.call"},
		// Four minutes of nothing: the model thinking, and real time spent.
		spec{off: 4*time.Minute + 25*time.Second, kind: "memory.written"},
	))
	require.True(t, strip.Show)

	classes := make([]string, 0, len(strip.Blocks))
	for _, b := range strip.Blocks {
		classes = append(classes, b.Class)
	}
	require.Equal(t, []string{"tool", "idle", "write"}, classes,
		"three calls under 30s apart merge into one block; the gap past it becomes idle")

	burst := strip.Blocks[0]
	require.Contains(t, burst.Title, "3 events")
	require.Contains(t, burst.Title, "0:00 to 0:25")
	require.Equal(t, "calls &times;3", burst.Label, "a wide block carries its count")

	idle := strip.Blocks[1]
	require.Greater(t, idle.Pct, 80.0, "the quiet is most of this session")
	require.Empty(t, idle.AnchorID, "there is no event under a gap to navigate to")
}

func TestBuildSessionTimeline_NarrowBlocksDropTheLabelNotTheTooltip(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	strip := buildSessionTimeline(rowsAt(base,
		spec{off: 0, kind: "tool.call"},
		spec{off: time.Second, kind: "tool.call"},
		spec{off: 20 * time.Minute, kind: "tool.call"},
	))
	require.True(t, strip.Show)
	first := strip.Blocks[0]
	require.Less(t, first.Pct, ptlLabelPct)
	require.Empty(t, first.Label, "a sliver would clip its label")
	require.NotEmpty(t, first.Title, "but the tooltip still says what it was")
}

func TestBuildSessionTimeline_RefusesToDrawWithoutASpan(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	require.False(t, buildSessionTimeline(nil).Show)
	require.False(t, buildSessionTimeline(rowsAt(base, spec{off: 0, kind: "tool.call"})).Show)
	// Every event at the same instant: equal blocks would be the uniform strip
	// this replaces, wearing a measurement's clothes.
	require.False(t, buildSessionTimeline(rowsAt(base,
		spec{off: 0, kind: "tool.call"},
		spec{off: 0, kind: "retrieval.injected"},
	)).Show)
}

func TestBuildSessionTimeline_AFailedCallBreaksTheBurst(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	strip := buildSessionTimeline(rowsAt(base,
		spec{off: 0, kind: "tool.call"},
		spec{off: 10 * time.Second, kind: "tool.call"},
		spec{off: 20 * time.Second, kind: "tool.call", isErr: true},
		spec{off: 30 * time.Second, kind: "tool.call"},
	))
	classes := make([]string, 0, len(strip.Blocks))
	for _, b := range strip.Blocks {
		classes = append(classes, b.Class)
	}
	require.Equal(t, []string{"tool", "error", "tool"}, classes,
		"a failure is its own block, not swallowed into the burst around it")
}

func TestPtlClass_ErrorsWinOverKind(t *testing.T) {
	require.Equal(t, "error", ptlClass("tool.call", true))
	require.Equal(t, "tool", ptlClass("tool.call", false))
	require.Equal(t, "inject", ptlClass("retrieval.injected", false))
	require.Equal(t, "write", ptlClass("memory.written", false))
	require.Equal(t, "write", ptlClass("note.written", false))
	require.Equal(t, "tool", ptlClass("session.started", false))
}

func TestPtlTrack_EveryBlockIsReachableAndTitled(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	strip := buildSessionTimeline(rowsAt(base,
		spec{off: 0, kind: "tool.call"},
		spec{off: 30 * time.Second, kind: "tool.call"},
		spec{off: 5 * time.Minute, kind: "retrieval.injected"},
	))
	html := string(ptlTrack(strip))
	require.Contains(t, html, `class="ptl-track"`)
	require.Contains(t, html, `data-class="tool"`)
	require.Contains(t, html, `data-class="idle"`)
	require.Contains(t, html, "title=")
	// Event blocks are anchors (keyboard-reachable without extra wiring); idle
	// stretches are inert spans.
	require.Equal(t, strings.Count(html, "<a "), strings.Count(html, "data-ptl-anchor="))
	require.Contains(t, html, "<span data-class=\"idle\"")
	require.Empty(t, string(ptlTrack(sessionTimeline{})))
}
