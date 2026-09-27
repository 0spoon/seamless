package console

import (
	"fmt"
	"hash/fnv"
	"html/template"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// The knowledge sky: the Overview's picture of everything the fleet knows.
// Every active memory is a star -- clustered into one galaxy per project scope,
// coloured by kind, sized by how much it is actually used (utility, then
// injections), and twinkling when it reached an agent in the last day. The page
// script flares a star the moment an injection names it over the live stream.
//
// The layout is deterministic: the same memories and stats always produce the
// same positions, so a live morph only touches what changed (a star brightens,
// a new one is born) instead of reshuffling the sky.

const (
	skyW, skyH   = 1200.0, 330.0
	skyMaxStars  = 600
	goldenAngle  = 2.399963229728653 // radians
	skyWarmFresh = 24 * time.Hour
)

// skyStar is one memory as the sky draws it.
type skyStar struct {
	ID, Name, Kind, Project string
	Utility                 float64
	Injects                 int
	LastInjected            *time.Time
	score                   float64
}

// skyData is the Overview panel payload. Shown can be below Total when the
// fleet has more memories than the sky draws (skyMaxStars).
type skyData struct {
	SVG      template.HTML
	Shown    int
	Total    int
	Clusters int
}

// buildSky projects the active memories into the sky. It returns nil when
// there is nothing to draw, so the template can drop the panel.
func buildSky(mems []core.Memory, stats map[string]store.RetrievalStat, now time.Time) *skyData {
	stars := make([]skyStar, 0, len(mems))
	for _, m := range mems {
		if !m.Active() {
			continue
		}
		st := stats[m.ID]
		s := skyStar{
			ID: m.ID, Name: m.Name, Kind: string(m.Kind), Project: m.Project,
			Utility: st.Utility, Injects: st.InjectCount, LastInjected: st.LastInjectedAt,
		}
		s.score = s.Utility*4 + math.Log1p(float64(s.Injects))
		if fresh(s.LastInjected, now) {
			s.score += 0.6
		}
		stars = append(stars, s)
	}
	if len(stars) == 0 {
		return nil
	}
	total := len(stars)
	sort.SliceStable(stars, func(i, j int) bool {
		if stars[i].score != stars[j].score {
			return stars[i].score > stars[j].score
		}
		return stars[i].ID < stars[j].ID
	})
	if len(stars) > skyMaxStars {
		stars = stars[:skyMaxStars]
	}

	// One galaxy per scope, largest first.
	byProject := map[string][]skyStar{}
	var order []string
	for _, s := range stars {
		if _, ok := byProject[s.Project]; !ok {
			order = append(order, s.Project)
		}
		byProject[s.Project] = append(byProject[s.Project], s)
	}
	sort.SliceStable(order, func(i, j int) bool {
		ni, nj := len(byProject[order[i]]), len(byProject[order[j]])
		if ni != nj {
			return ni > nj
		}
		return order[i] < order[j]
	})
	maxScore := stars[0].score
	if maxScore <= 0 {
		maxScore = 1
	}
	nMax := len(byProject[order[0]])

	// Slots run centre-out, so the biggest galaxy sits in the middle of the sky
	// and the smaller ones flank it alternately.
	k := len(order)
	slotOf := make([]int, k)
	mid := (k - 1) / 2
	for i := range order {
		off := (i + 1) / 2
		if i%2 == 1 {
			slotOf[i] = mid - off
		} else {
			slotOf[i] = mid + off
		}
	}
	slotW := skyW / float64(k)
	rMax := math.Min(slotW*0.44, skyH*0.40)

	var b strings.Builder
	alt := fmt.Sprintf("Knowledge sky: %d active memories across %d scopes; each star is a memory, brighter ones are used more", total, k)
	fmt.Fprintf(&b, `<svg class="sky-svg" viewBox="0 0 %g %g" preserveAspectRatio="xMidYMid meet" role="img" aria-label="%s">`, skyW, skyH, template.HTMLEscapeString(alt))
	b.WriteString(`<defs><filter id="sky-glow" x="-200%" y="-200%" width="500%" height="500%"><feGaussianBlur stdDeviation="2.4" result="b"/><feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge></filter>`)
	for i, p := range order {
		fmt.Fprintf(&b, `<radialGradient id="sky-neb-%d"><stop offset="0" style="stop-color:%s;stop-opacity:.22"/><stop offset=".55" style="stop-color:%s;stop-opacity:.06"/><stop offset="1" style="stop-color:%s;stop-opacity:0"/></radialGradient>`,
			i, dominantKindColor(byProject[p]), dominantKindColor(byProject[p]), dominantKindColor(byProject[p]))
	}
	b.WriteString(`</defs>`)

	var labels strings.Builder
	for i, p := range order {
		group := byProject[p]
		n := len(group)
		r := rMax * (0.42 + 0.58*math.Sqrt(float64(n)/float64(nMax)))
		cx := slotW*(float64(slotOf[i])+0.5) + jitter(p, 7)
		cy := skyH/2 - 8 + 22*math.Sin(float64(slotOf[i])*1.9+0.6) + jitter(p+"y", 5)
		rot := jitter(p+"r", math.Pi)

		fmt.Fprintf(&b, `<circle class="sky-neb" cx="%.1f" cy="%.1f" r="%.1f" fill="url(#sky-neb-%d)"/>`, cx, cy, r*1.45, i)

		type pt struct{ x, y, ang float64 }
		pts := make([]pt, n)
		for j := range group {
			rr := r * math.Sqrt((float64(j)+0.6)/float64(n))
			th := float64(j)*goldenAngle + rot
			pts[j] = pt{cx + rr*math.Cos(th), cy + rr*math.Sin(th)*0.8, 0}
		}

		// Constellation lines through the brightest few, walked by angle so
		// the figure closes into a shape instead of zig-zagging.
		if n >= 3 {
			top := min(n, 6)
			fig := make([]pt, top)
			copy(fig, pts[:top])
			for j := range fig {
				fig[j].ang = math.Atan2(fig[j].y-cy, fig[j].x-cx)
			}
			sort.Slice(fig, func(a, c int) bool { return fig[a].ang < fig[c].ang })
			var d strings.Builder
			for j, f := range fig {
				if j == 0 {
					fmt.Fprintf(&d, "M%.1f %.1f", f.x, f.y)
				} else {
					fmt.Fprintf(&d, " L%.1f %.1f", f.x, f.y)
				}
			}
			if top >= 4 {
				d.WriteString(" Z")
			}
			fmt.Fprintf(&b, `<path class="sky-line" d="%s"/>`, d.String())
		}

		for j, s := range group {
			u := s.score / maxScore
			rad := 1.15 + 2.7*math.Sqrt(math.Max(u, 0))
			cls := "star"
			if u > 0.55 {
				cls += " bright"
			}
			if fresh(s.LastInjected, now) {
				cls += " warm"
			}
			fmt.Fprintf(&b, `<a href="/console/memories/%s" tabindex="-1" class="star-link"><circle class="%s" data-id="%s" data-name="%s" data-kind="%s" data-meta="%s" cx="%.1f" cy="%.1f" r="%.2f" style="fill:%s;--tw:%.2fs"/></a>`,
				template.HTMLEscapeString(s.ID), cls, template.HTMLEscapeString(s.ID), template.HTMLEscapeString(s.Name),
				template.HTMLEscapeString(s.Kind), template.HTMLEscapeString(skyMeta(s)), pts[j].x, pts[j].y, rad,
				kindColorVar(s.Kind), -jitterPos(s.ID, 6))
		}

		name := p
		if name == "" {
			name = "shared globally"
		}
		ly := math.Min(cy+r*0.8+24, skyH-8)
		fmt.Fprintf(&labels, `<text class="sky-label" x="%.1f" y="%.1f" text-anchor="middle">%s<tspan class="sky-count" dx="7">%d</tspan></text>`,
			cx, ly, template.HTMLEscapeString(name), n)
	}
	b.WriteString(labels.String())
	b.WriteString(`</svg>`)
	return &skyData{SVG: template.HTML(b.String()), Shown: len(stars), Total: total, Clusters: k}
}

// skyMeta is the tooltip's second line: kind, scope, and use.
func skyMeta(s skyStar) string {
	scope := s.Project
	if scope == "" {
		scope = "global"
	}
	use := "never surfaced"
	if s.Injects > 0 {
		use = fmt.Sprintf("surfaced %d×", s.Injects)
	}
	return s.Kind + " · " + scope + " · " + use
}

// dominantKindColor tints a galaxy's nebula with its most common kind.
func dominantKindColor(group []skyStar) string {
	counts := map[string]int{}
	best, bestN := "", -1
	for _, s := range group {
		counts[s.Kind]++
	}
	for _, kd := range core.MemoryKinds {
		if n := counts[string(kd)]; n > bestN {
			best, bestN = string(kd), n
		}
	}
	return kindColorVar(best)
}

func fresh(t *time.Time, now time.Time) bool {
	return t != nil && !t.IsZero() && now.Sub(*t) <= skyWarmFresh
}

// jitter is a stable pseudo-random offset in [-amp, amp] derived from a key,
// so the sky looks organic yet renders identically every time.
func jitter(key string, amp float64) float64 {
	return (jitterPos(key, 2) - 1) * amp
}

// jitterPos is a stable pseudo-random value in [0, max).
func jitterPos(key string, max float64) float64 {
	h := fnv.New32a()
	h.Write([]byte(key)) //nolint:errcheck // hash.Hash.Write never returns an error (documented contract)
	return float64(h.Sum32()%10000) / 10000 * max
}
