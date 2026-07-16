package console

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/store"
)

// The .area-line draw-on animation rides on pathLength="1": the CSS sets
// stroke-dasharray:1 so one dash spans the whole path, then animates the offset
// to 0. vector-effect="non-scaling-stroke" moves the dash pattern into screen
// space, which defeats that normalization -- the dash then covers only 1/scale of
// the path, so the line's tail is silently clipped at every width past the
// 560-unit viewBox and the fill runs on without it. At the design width scale is
// 1.0 and the bug is invisible, which is exactly why it needs pinning here.
//
// The gridlines and the peak marker keep non-scaling-stroke: they carry no
// pathLength, so a screen-space dash is what they actually want.
func TestAreaChart_LineHasNoNonScalingStroke(t *testing.T) {
	got := string(areaChart([]store.TrendBucket{
		{Label: "Jul 10", Count: 1},
		{Label: "Jul 13", Count: 9},
		{Label: "Jul 15", Count: 4},
	}))

	line := svgTag(t, got, `<path class="area-line"`)
	require.Contains(t, line, `pathLength="1"`, "the draw-on animation needs the normalization")
	require.NotContains(t, line, "non-scaling-stroke",
		"non-scaling-stroke + pathLength clips the line's tail as the card widens")

	// The peak marker's leader line is the control: it wants the screen-space stroke.
	require.Contains(t, svgTag(t, got, `<g class="area-peak"><line`), "non-scaling-stroke")
}

// svgTag returns prefix plus the rest of the tag it ends inside, so a test can
// assert on one element's attributes without matching the whole chart. The scan
// for the tag end starts after the prefix, so a prefix may span into a nested
// element (`<g class="x"><line`) to reach the tag that carries the attributes.
func svgTag(t *testing.T, markup, prefix string) string {
	t.Helper()
	i := strings.Index(markup, prefix)
	require.GreaterOrEqual(t, i, 0, "chart is missing %s", prefix)
	rest := markup[i:]
	end := strings.Index(rest[len(prefix):], ">")
	require.GreaterOrEqual(t, end, 0, "unterminated tag at %s", prefix)
	return rest[:len(prefix)+end+1]
}
