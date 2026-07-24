package retrieve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/store"
)

// fullConventionLines counts the full-tier "CONVENTION:" lines in a briefing.
func fullConventionLines(b string) int { return strings.Count(b, "CONVENTION: ") }

// The CONVENTION section tiers at convention_max_full: the top K conventions
// (recency order with the utility gate closed) render as full lines above the
// memory index, the rest stay behind the count line, and only the rendered
// lines' ids reach the read-after-inject funnel -- the hidden ones are named
// nowhere, so recording them would fake exposure.
func TestConventionSectionTierAndCount(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01RUL", "constraint", "hard-rule", "the one rule", "p", now.Add(-1*time.Hour))
	insMemAt(t, db, "01CVA", "convention", "conv-a", "layout fact a", "p", now.Add(-1*time.Hour))
	insMemAt(t, db, "01CVB", "convention", "conv-b", "layout fact b", "p", now.Add(-2*time.Hour))
	insMemAt(t, db, "01CVC", "convention", "conv-c", "layout fact c", "p", now.Add(-3*time.Hour))
	insMemAt(t, db, "01CVD", "convention", "conv-d", "layout fact d", "p", now.Add(-4*time.Hour))

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConventionMaxFull = 2 }))

	b, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Equal(t, 2, fullConventionLines(b), "exactly K full lines")
	require.Contains(t, b, "CONVENTION: conv-a: layout fact a")
	require.Contains(t, b, "CONVENTION: conv-b: layout fact b")
	require.Contains(t, b, "(4 conventions, 2 shown -- recall kind=convention for the rest)")
	require.NotContains(t, b, "conv-c", "hidden conventions are named nowhere")
	require.NotContains(t, b, "conv-d")
	require.Subset(t, ids, []string{"01CVA", "01CVB"})
	require.NotContains(t, ids, "01CVC", "hidden conventions must not fake funnel exposure")
	require.NotContains(t, ids, "01CVD")

	// Conventions leave the memory index entirely and render above it; the
	// constraint head is untouched.
	require.NotContains(t, b, "- conv-a")
	require.Contains(t, b, "CONSTRAINT: hard-rule: the one rule")
	require.Equal(t, 1, strings.Count(b, "conv-a:"), "a convention renders exactly once")
}

// convention_max_full=0 disables the tiering, matching ConstraintMaxFull
// semantics: every convention renders full and the count line still closes the
// section with the retrieval mechanism.
func TestConventionTierZeroDisables(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01CVA", "convention", "conv-a", "layout fact a", "p", now.Add(-1*time.Hour))
	insMemAt(t, db, "01CVB", "convention", "conv-b", "layout fact b", "p", now.Add(-2*time.Hour))
	insMemAt(t, db, "01CVC", "convention", "conv-c", "layout fact c", "p", now.Add(-3*time.Hour))

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConventionMaxFull = 0 }))

	b, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Equal(t, 3, fullConventionLines(b))
	require.Contains(t, b, "(3 conventions -- recall kind=convention)")
	require.Subset(t, ids, []string{"01CVA", "01CVB", "01CVC"})
}

// A starred convention is pinned into the FAVORITE section, never demoted to
// the budget-competing CONVENTION block, and never rendered twice; the
// section's pool shrinks accordingly.
func TestConventionStarredPinsAsFavorite(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01STA", "convention", "starred-conv", "starred layout fact", "p", now.Add(-1*time.Hour))
	insMemAt(t, db, "01PLA", "convention", "plain-conv", "plain layout fact", "p", now.Add(-2*time.Hour))
	_, err := db.ExecContext(ctx, `UPDATE memories_index SET favorite = 1 WHERE id = '01STA'`)
	require.NoError(t, err)

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConventionMaxFull = 4 }))

	b, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Contains(t, b, "FAVORITE: starred-conv: starred layout fact")
	require.Equal(t, 1, strings.Count(b, "starred-conv"), "starred convention renders once, as a favorite")
	require.Equal(t, 1, fullConventionLines(b))
	require.Contains(t, b, "(1 conventions -- recall kind=convention)")
	require.Subset(t, ids, []string{"01STA", "01PLA"})
}

// Conventions are budget-competing: a starved budget drops the full lines, but
// the count line still renders, so the section is never invisible and the pool
// stays one kind-filtered recall away.
func TestConventionCountLineSurvivesBudget(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01CVA", "convention", "conv-a", strings.Repeat("wide description ", 30), "p", now.Add(-1*time.Hour))
	insMemAt(t, db, "01CVB", "convention", "conv-b", strings.Repeat("wide description ", 30), "p", now.Add(-2*time.Hour))

	svc := New(db, nil, config.Budgets{MaxBriefingTokens: 1}, nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConventionMaxFull = 2 }))

	b, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Equal(t, 0, fullConventionLines(b), "budget drops every full line")
	require.Contains(t, b, "(2 conventions, 0 shown -- recall kind=convention for the rest)")
	require.NotContains(t, ids, "01CVA")
	require.NotContains(t, ids, "01CVB")
}
