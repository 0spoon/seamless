package retrieve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/agentguide"
	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/store"
)

// The subagent footer renders whenever the briefing renders, immediately
// before the closing tag and exactly once, independent of where the tier
// boundary falls -- below the cap, at it, above it, and with tiering disabled.
func TestSubagentFooter_AlwaysOnAcrossTierSplits(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01ONE", "constraint", "c-one", "rule one", "p", now.Add(-1*time.Hour))
	insMemAt(t, db, "01TWO", "constraint", "c-two", "rule two", "p", now.Add(-2*time.Hour))
	insMemAt(t, db, "01THR", "constraint", "c-three", "rule three", "p", now.Add(-3*time.Hour))

	svc := New(db, nil, budgets(), nil)

	tests := []struct {
		name        string
		maxFull     int
		wantCompact bool
	}{
		{name: "below the cap", maxFull: 5, wantCompact: false},
		{name: "exactly at the cap", maxFull: 3, wantCompact: false},
		{name: "above the cap", maxFull: 2, wantCompact: true},
		{name: "tiering disabled", maxFull: 0, wantCompact: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = tt.maxFull }))
			sb, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", AgentType: "Explore"})
			require.NoError(t, err)
			require.Contains(t, sb, "(subagent scope)")
			require.True(t, strings.HasSuffix(sb, subagentFooter+"</seam-briefing>"),
				"footer must sit immediately before the closing tag: %s", sb)
			require.Equal(t, 1, strings.Count(sb, subagentFooter), "footer renders exactly once")
			if tt.wantCompact {
				// The compact line's wording stays byte-identical alongside the
				// footer; their mild redundancy is accepted by design.
				require.Contains(t, sb, "Also binding (1): c-three -- memory_read a name before working near it.")
			} else {
				require.NotContains(t, sb, "Also binding")
			}
			require.ElementsMatch(t, []string{"01ONE", "01TWO", "01THR"}, ids)
		})
	}
}

// A subagent briefing with no constraints in scope stays empty: the footer
// never renders on its own.
func TestSubagentFooter_NoConstraintsNoFooter(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))
	insMemAt(t, db, "01GOT", "gotcha", "not-a-constraint", "children never see gotchas", "p", time.Now())

	svc := New(db, nil, budgets(), nil)
	sb, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", AgentType: "Explore"})
	require.NoError(t, err)
	require.Empty(t, sb)
	require.Empty(t, ids)
}

// The footer mirrors the main briefing's vocabulary: byte-identical to the
// first sentence of agentguide.BriefingFooter, so the two surfaces cannot
// drift apart silently.
func TestSubagentFooter_MirrorsMainFooterVocabulary(t *testing.T) {
	require.True(t, strings.HasPrefix(agentguide.BriefingFooter, strings.TrimSuffix(subagentFooter, "\n")),
		"subagentFooter must stay the first sentence of agentguide.BriefingFooter")
}

// The main-session briefing is untouched: it keeps the full BriefingFooter
// and never renders the standalone subagent footer line.
func TestSubagentFooter_MainBriefingUnchanged(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))
	insMemAt(t, db, "01ONE", "constraint", "c-one", "rule one", "p", time.Now())

	svc := New(db, nil, budgets(), nil)
	b, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Contains(t, b, agentguide.BriefingFooter)
	require.NotContains(t, b, subagentFooter,
		"the standalone subagent footer line must not appear in main briefings")
}
