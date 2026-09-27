package console

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// The Overview draws every active memory as a star in its scope's galaxy, and
// the kind legend under it; the JSON contract carries none of it.
func TestOverview_RendersKnowledgeSky(t *testing.T) {
	_, mgr, mux := newConsoleWithFiles(t)
	a := writeMemory(t, mgr, core.KindGotcha, "seamless", "sky-gotcha", "a gotcha")
	b := writeMemory(t, mgr, core.KindDecision, "", "sky-global-decision", "a global decision")

	page := getPeek(t, mux, "/console/")
	require.Equal(t, http.StatusOK, page.Code)
	body := page.Body.String()
	require.Contains(t, body, `class="sky-svg"`)
	require.Contains(t, body, `data-id="`+a.ID+`"`)
	require.Contains(t, body, `data-id="`+b.ID+`"`)
	require.Contains(t, body, `href="/console/memories/`+a.ID+`"`, "a star opens its memory")
	require.Contains(t, body, ">shared globally<", "the global scope is its own galaxy")
	require.Contains(t, body, `class="sky-kind"`, "the kind legend rides under the sky")
	require.NotContains(t, body, `aria-label="Memory fabric"`, "the sky replaces the separate kind panel")

	var data map[string]any
	getJSON(t, mux, "/console/?format=json", &data)
	_, hasSky := data["Sky"]
	require.False(t, hasSky, "the sky is HTML-only")
}

// The layout is a pure function of the memories and their stats: the same
// input renders byte-identical SVG, so a live morph moves nothing that did not
// change.
func TestBuildSky_Deterministic(t *testing.T) {
	now := time.Now()
	var mems []core.Memory
	for i, k := range []core.MemoryKind{core.KindGotcha, core.KindDecision, core.KindRunbook, core.KindConstraint} {
		for j := range 5 {
			id, err := core.NewID()
			require.NoError(t, err)
			mems = append(mems, core.Memory{ID: id, Kind: k, Name: strings.Repeat("m", i+j+1), Project: []string{"a", "b", ""}[j%3]})
		}
	}
	stats := map[string]store.RetrievalStat{mems[0].ID: {InjectCount: 9, Utility: 0.8, LastInjectedAt: &now}}
	one := buildSky(mems, stats, now)
	two := buildSky(mems, stats, now)
	require.NotNil(t, one)
	require.Equal(t, one.SVG, two.SVG)
	require.Equal(t, 20, one.Total)
	require.Equal(t, 3, one.Clusters)
	require.Contains(t, string(one.SVG), `class="star bright warm"`, "the most-used, freshly injected memory is bright and warm")
	require.Nil(t, buildSky(nil, nil, now), "no active memories, no sky")
}
