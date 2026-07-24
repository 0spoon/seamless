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

// constraintsSectionOf extracts the briefing's Constraints section body: the
// lines between its header and the blank line before the next section (or the
// rest of the briefing when it is the last section).
func constraintsSectionOf(b string) string {
	_, after, ok := strings.Cut(b, "Constraints (binding for every session):\n")
	if !ok {
		return ""
	}
	sec, _, _ := strings.Cut(after, "\n\n")
	return sec
}

// fullConstraintLines counts the full-tier bullets in a briefing's Constraints
// section (the "- +N more" compact line is not a full line).
func fullConstraintLines(b string) int {
	n := 0
	for line := range strings.SplitSeq(constraintsSectionOf(b), "\n") {
		if strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "- +") {
			n++
		}
	}
	return n
}

// The tier boundary sits exactly at constraint_max_full: the top K constraints
// (recency order with the utility gate closed) render as full lines, the rest
// collapse into one "equally binding" line -- and every name plus every id stays
// visible regardless of K.
func TestConstraintTierBoundaryAtK(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01ONE", "constraint", "c-one", "rule one", "p", now.Add(-1*time.Hour))
	insMemAt(t, db, "01TWO", "constraint", "c-two", "rule two", "p", now.Add(-2*time.Hour))
	insMemAt(t, db, "01THR", "constraint", "c-three", "rule three", "p", now.Add(-3*time.Hour))
	insMemAt(t, db, "01FOU", "constraint", "c-four", "rule four", "p", now.Add(-4*time.Hour))

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 2 }))

	b, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Equal(t, 2, fullConstraintLines(b), "exactly K full lines")
	require.Contains(t, b, "- c-one: rule one")
	require.Contains(t, b, "- c-two: rule two")
	require.Contains(t, b, "- +2 more, equally binding -- memory_read name=<name> before working near one: c-three, c-four")
	// The compact tier's ids are recorded alongside the full tier's, so the
	// read-after-inject funnel keeps seeing every constraint.
	require.Subset(t, ids, []string{"01ONE", "01TWO", "01THR", "01FOU"})

	// At K == len there is no compact tier; past it neither.
	for _, k := range []int{4, 5} {
		svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = k }))
		b, _, err = svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
		require.NoError(t, err)
		require.Equal(t, 4, fullConstraintLines(b))
		require.NotContains(t, b, "equally binding")
	}

	// K just below len collapses exactly the one overflow constraint.
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 3 }))
	b, _, err = svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Equal(t, 3, fullConstraintLines(b))
	require.Contains(t, b, "- +1 more, equally binding -- memory_read name=<name> before working near one: c-four")

	// Every constraint name is present in every briefing regardless of K.
	for _, k := range []int{0, 1, 2, 3, 4, 50} {
		svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = k }))
		b, ids, err = svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
		require.NoError(t, err)
		for _, name := range []string{"c-one", "c-two", "c-three", "c-four"} {
			require.Contains(t, b, name, "K=%d must keep every constraint name visible", k)
		}
		require.Subset(t, ids, []string{"01ONE", "01TWO", "01THR", "01FOU"},
			"K=%d must record every constraint id", k)
	}
}

// A starred constraint claims a full-tier slot ahead of fresher unstarred
// ones; the displaced constraints keep their recency order in the compact tier.
func TestConstraintTierFavoritePromoted(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01STA", "constraint", "starred-rule", "old but starred", "p", now.Add(-30*24*time.Hour))
	insMemAt(t, db, "01FRA", "constraint", "fresh-a", "fresh unstarred a", "p", now.Add(-1*time.Hour))
	insMemAt(t, db, "01FRB", "constraint", "fresh-b", "fresh unstarred b", "p", now.Add(-2*time.Hour))
	_, err := db.ExecContext(ctx, `UPDATE memories_index SET favorite = 1 WHERE id = '01STA'`)
	require.NoError(t, err)

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 1 }))

	b, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Equal(t, 1, fullConstraintLines(b))
	require.Contains(t, b, "- starred-rule: old but starred")
	require.Contains(t, b, "- +2 more, equally binding -- memory_read name=<name> before working near one: fresh-a, fresh-b")
	require.Subset(t, ids, []string{"01STA", "01FRA", "01FRB"})
}

// constraint_max_full=0 disables tiering entirely: every constraint renders as
// a full line in the legacy updated_at DESC order -- no compact line, and no
// favorite promotion reordering either.
func TestConstraintTierZeroDisables(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01STA", "constraint", "starred-rule", "old but starred", "p", now.Add(-30*24*time.Hour))
	insMemAt(t, db, "01FRA", "constraint", "fresh-a", "fresh unstarred a", "p", now.Add(-1*time.Hour))
	insMemAt(t, db, "01FRB", "constraint", "fresh-b", "fresh unstarred b", "p", now.Add(-2*time.Hour))
	_, err := db.ExecContext(ctx, `UPDATE memories_index SET favorite = 1 WHERE id = '01STA'`)
	require.NoError(t, err)

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 0 }))

	b, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Equal(t, 3, fullConstraintLines(b))
	require.NotContains(t, b, "equally binding")
	a, bb, c := strings.Index(b, "- fresh-a"), strings.Index(b, "- fresh-b"), strings.Index(b, "- starred-rule")
	require.True(t, a >= 0 && bb >= 0 && c >= 0)
	require.True(t, a < bb && bb < c, "legacy order is updated_at DESC, star not promoted: %s", b)
}

// Subagent briefings tier identically: same boundary, same compact line, and
// the compact tier's ids are still reported for instrumentation.
func TestConstraintTierSubagentParity(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01ONE", "constraint", "c-one", "rule one", "p", now.Add(-1*time.Hour))
	insMemAt(t, db, "01TWO", "constraint", "c-two", "rule two", "p", now.Add(-2*time.Hour))
	insMemAt(t, db, "01THR", "constraint", "c-three", "rule three", "p", now.Add(-3*time.Hour))

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 2 }))

	sb, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", AgentType: "Explore"})
	require.NoError(t, err)
	require.Contains(t, sb, "(subagent scope)")
	require.Equal(t, 2, fullConstraintLines(sb))
	require.Contains(t, sb, "- c-one: rule one")
	require.Contains(t, sb, "- c-two: rule two")
	require.Contains(t, sb, "- +1 more, equally binding -- memory_read name=<name> before working near one: c-three")
	require.ElementsMatch(t, []string{"01ONE", "01TWO", "01THR"}, ids)

	// The 0-disables path holds for subagents too.
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 0 }))
	sb, ids, err = svc.Briefing(ctx, BriefingInput{CWD: "/w", AgentType: "Explore"})
	require.NoError(t, err)
	require.Equal(t, 3, fullConstraintLines(sb))
	require.NotContains(t, sb, "equally binding")
	require.ElementsMatch(t, []string{"01ONE", "01TWO", "01THR"}, ids)
}

// The tier ranking sits behind the same utility gate as the memory index: with
// the gate closed (auto, no latch) pure recency decides the boundary; with
// ranking active the blended key lifts a proven old constraint into the full
// tier and demotes the least-recent unproven one to the compact line.
func TestConstraintTierUtilityGateFallsBackToRecency(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01HOT", "constraint", "hot-rule", "old proven workhorse", "p", now.Add(-10*24*time.Hour))
	insMemAt(t, db, "01MID", "constraint", "mid-rule", "two days old", "p", now.Add(-2*24*time.Hour))
	insMemAt(t, db, "01FRE", "constraint", "fresh-rule", "just written", "p", now)
	seedReads(t, db, "01HOT", 6)

	svc := New(db, nil, budgets(), nil)

	// Gate closed (default auto, nothing latched): the tier degrades to pure
	// recency -- the ten-day-old constraint drops to the compact line no matter
	// how hot it is.
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 2 }))
	b, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Contains(t, b, "- fresh-rule")
	require.Contains(t, b, "- mid-rule")
	require.Contains(t, b, "- +1 more, equally binding -- memory_read name=<name> before working near one: hot-rule")

	// Gate open (mode on): the blended key lifts the proven constraint into the
	// full tier; the two-day-old unproven one takes the compact line instead.
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 2; b.UtilityMode = "on" }))
	b, _, err = svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Contains(t, b, "- hot-rule")
	require.Contains(t, b, "- fresh-rule")
	require.Contains(t, b, "- +1 more, equally binding -- memory_read name=<name> before working near one: mid-rule")
}
