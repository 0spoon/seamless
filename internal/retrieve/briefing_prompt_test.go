package retrieve

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/store"
)

// These tests cover the subagent briefing's RELEVANT section
// (plan:subagent-briefing, D3): BriefingInput.Prompt is matched against the
// project's memories (all kinds) and rendered as up to subagentRelevantMax
// lines between the constraint tiers and the footer. They replace the retired
// TestSubagentBriefing_PromptFieldUnread, which pinned the field unread until
// this step landed.

// relevantSvc seeds a project with one constraint plus prompt-matchable
// memories of other kinds and returns a service over it.
func relevantSvc(t *testing.T) (*Service, context.Context) {
	t.Helper()
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/work/seam":"seam"}`))
	insMem(t, db, "01CON", "constraint", "no-force-push", "never force push to main", "seam")
	insMem(t, db, "01GOT", "gotcha", "chroma-boot-race", "chroma container health check startup race", "seam")
	insMem(t, db, "01RUN", "runbook", "compose-restart-order", "restart the chroma container health check after compose startup race", "seam")
	insMem(t, db, "01REF", "reference", "unrelated-fact", "postgres vacuum tuning notes", "seam")
	return New(db, nil, budgets(), nil), ctx
}

func TestSubagentBriefing_RelevantSectionRenders(t *testing.T) {
	svc, ctx := relevantSvc(t)

	sb, ids, err := svc.Briefing(ctx, BriefingInput{
		CWD: "/work/seam", AgentType: "Explore",
		Prompt: "fix the chroma container health check race at compose startup",
	})
	require.NoError(t, err)
	require.Contains(t, sb, "RELEVANT: chroma-boot-race: chroma container health check startup race")
	require.Contains(t, sb, "RELEVANT: compose-restart-order:")
	require.NotContains(t, sb, "unrelated-fact", "non-matching memories stay out")

	// The section sits between the constraint tiers and the footer, which
	// stays the last line before the closing tag.
	constraintAt := strings.Index(sb, "CONSTRAINT: no-force-push")
	relevantAt := strings.Index(sb, "RELEVANT: ")
	require.Greater(t, relevantAt, constraintAt, "RELEVANT renders after the constraint tiers")
	require.True(t, strings.HasSuffix(sb, subagentFooter+"</seam-briefing>"),
		"the footer stays the last line before the closing tag: %s", sb)

	// All RELEVANT ids join the injected-ids instrumentation.
	require.Contains(t, ids, "01CON")
	require.Contains(t, ids, "01GOT")
	require.Contains(t, ids, "01RUN")
	require.NotContains(t, ids, "01REF")
}

// An empty (unresolved) spawn prompt means no section -- and never a failed
// briefing.
func TestSubagentBriefing_EmptyPromptNoSection(t *testing.T) {
	svc, ctx := relevantSvc(t)

	for _, prompt := range []string{"", "   \n\t "} {
		sb, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/work/seam", AgentType: "Explore", Prompt: prompt})
		require.NoError(t, err)
		require.NotEmpty(t, sb)
		require.NotContains(t, sb, "RELEVANT:")
		require.Equal(t, []string{"01CON"}, ids, "only the constraint is injected without a prompt")
	}
}

// A prompt that matches nothing yields no section, not an empty header line.
func TestSubagentBriefing_NoMatchesNoSection(t *testing.T) {
	svc, ctx := relevantSvc(t)

	sb, ids, err := svc.Briefing(ctx, BriefingInput{
		CWD: "/work/seam", AgentType: "Explore",
		Prompt: "weather forecast for paris tomorrow morning",
	})
	require.NoError(t, err)
	require.NotEmpty(t, sb)
	require.NotContains(t, sb, "RELEVANT:")
	require.Equal(t, []string{"01CON"}, ids)
}

// A constraint the child already sees -- in either tier -- is never repeated
// as a RELEVANT line, and the dedupe never starves the section: hits ranked
// below the deduped constraint still fill it.
func TestSubagentBriefing_RelevantDedupesBothConstraintTiers(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))
	// The constraints carry the prompt's extra "debug" term, so they strictly
	// out-score the gotchas and claim the top of the raw ranking -- exactly the
	// arrangement where a pre-capped hit list would starve the section. The
	// unrelated fillers diversify the corpus so every candidate clears the IDF
	// score floor.
	insMem(t, db, "01CA", "constraint", "chroma-health-gate", "debug chroma container health check startup race", "p")
	insMem(t, db, "01CB", "constraint", "chroma-startup-order", "debug chroma container health check startup race", "p")
	insMem(t, db, "01GA", "gotcha", "chroma-race-gotcha", "chroma container health check startup race", "p")
	insMem(t, db, "01GB", "gotcha", "container-check-gotcha", "chroma container health check startup race", "p")
	insMem(t, db, "01X1", "reference", "postgres-vacuum", "postgres vacuum tuning autovacuum thresholds", "p")
	insMem(t, db, "01X2", "reference", "docs-deploy", "deploy the docs site through cloudflare pages", "p")
	svc := New(db, nil, budgets(), nil)

	const prompt = "debug the chroma container health check startup race"
	for _, tt := range []struct {
		name    string
		maxFull int
	}{
		{name: "both constraints in the full tier", maxFull: 0},
		{name: "one constraint in the compact tier", maxFull: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = tt.maxFull }))
			sb, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", AgentType: "Explore", Prompt: prompt})
			require.NoError(t, err)
			require.NotContains(t, sb, "RELEVANT: chroma-health-gate",
				"a constraint visible in either tier is never repeated as RELEVANT")
			require.NotContains(t, sb, "RELEVANT: chroma-startup-order",
				"a constraint visible in either tier is never repeated as RELEVANT")
			// The dedupe must not shrink the section below the genuinely-new
			// hits: both gotchas rank behind the two matching constraints, and
			// both still render.
			require.Contains(t, sb, "RELEVANT: chroma-race-gotcha")
			require.Contains(t, sb, "RELEVANT: container-check-gotcha")
			require.ElementsMatch(t, []string{"01CA", "01CB", "01GA", "01GB"}, ids,
				"every rendered memory joins the instrumentation exactly once")
		})
	}
}

// The section caps at subagentRelevantMax lines even when more memories match.
func TestSubagentBriefing_RelevantCapsAtMax(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))
	insMem(t, db, "01CON", "constraint", "hard-rule", "never break the rule", "p")
	for _, m := range []struct{ id, name string }{
		{"01M1", "race-one"}, {"01M2", "race-two"}, {"01M3", "race-three"},
		{"01M4", "race-four"}, {"01M5", "race-five"},
	} {
		insMem(t, db, m.id, "gotcha", m.name, "chroma container health check startup race", "p")
	}
	svc := New(db, nil, budgets(), nil)

	sb, ids, err := svc.Briefing(ctx, BriefingInput{
		CWD: "/w", AgentType: "Explore",
		Prompt: "chroma container health check race at startup",
	})
	require.NoError(t, err)
	require.Equal(t, subagentRelevantMax, strings.Count(sb, "RELEVANT: "))
	require.Len(t, ids, 1+subagentRelevantMax, "constraint plus the capped RELEVANT hits")
}

// No constraints in scope means no child briefing at all: RELEVANT hits alone
// never render (the section exists only where the constraint core does).
func TestSubagentBriefing_NoConstraintsNoBriefingDespiteMatches(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))
	insMem(t, db, "01GOT", "gotcha", "chroma-boot-race", "chroma container health check startup race", "p")
	svc := New(db, nil, budgets(), nil)

	sb, ids, err := svc.Briefing(ctx, BriefingInput{
		CWD: "/w", AgentType: "Explore",
		Prompt: "chroma container health check race at startup",
	})
	require.NoError(t, err)
	require.Empty(t, sb)
	require.Empty(t, ids)
}

// A matcher failure costs the section, never the briefing (failure-soft, the
// same posture as every other briefing-side read).
func TestSubagentRelevant_MatcherErrorFailureSoft(t *testing.T) {
	db := setupDB(t)
	svc := New(db, nil, budgets(), nil)
	require.NoError(t, db.Close())

	hits := svc.subagentRelevant(context.Background(), "p", "chroma container health check race", nil)
	require.Nil(t, hits, "an unbuildable corpus yields no section, not an error")
}

// With every section present -- full tier, compact tier, RELEVANT, footer --
// the subagent briefing stays within the hard cap and well-formed.
func TestSubagentBriefing_HardCapWithAllSections(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))
	for i := range 90 {
		id := "01C" + string(rune('A'+i/26)) + string(rune('A'+i%26))
		insMem(t, db, id, "constraint", "bulk-rule-"+id, strings.Repeat("bounded child detail ", 12), "p")
	}
	insMem(t, db, "01GOT", "gotcha", "chroma-boot-race", "chroma container health check startup race", "p")
	svc := New(db, nil, budgets(), nil)
	// Legacy all-full rendering overflows the cap; tiering would keep it under.
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 0 }))

	sb, ids, err := svc.Briefing(ctx, BriefingInput{
		CWD: "/w", AgentType: "Explore",
		Prompt: "chroma container health check race at startup",
	})
	require.NoError(t, err)
	// hardTruncate's ellipsis can round the estimate one token past the cap
	// (the historical contract); the point is the 4000+ token wall is gone.
	hardCap := svc.briefingHardCap(svc.briefing)
	require.LessOrEqual(t, estTokens(sb), hardCap+1, "the assembled briefing respects the hard cap")
	require.True(t, strings.HasSuffix(sb, "</seam-briefing>"), "truncation keeps the briefing well-formed")
	require.Contains(t, sb, "CONSTRAINT: bulk-rule-", "the pinned head survives the cap")
	// The ids stay the full superset (the assembleBriefing posture): the
	// RELEVANT hit is credited even when its line fell to the cap.
	require.Contains(t, ids, "01GOT")
	require.Len(t, ids, 91)
}

// The main-session path never reads Prompt: its briefing stays byte-identical
// with and without one.
func TestMainBriefing_PromptFieldIgnored(t *testing.T) {
	svc, ctx := relevantSvc(t)

	main := BriefingInput{CWD: "/work/seam", Source: "startup"}
	without, _, err := svc.Briefing(ctx, main)
	require.NoError(t, err)
	require.NotEmpty(t, without)

	prompted := main
	prompted.Prompt = "fix the chroma container health check race at compose startup"
	with, _, err := svc.Briefing(ctx, prompted)
	require.NoError(t, err)
	require.Equal(t, without, with, "a main-session briefing must ignore Prompt")
	require.NotContains(t, without, "RELEVANT:")
}
