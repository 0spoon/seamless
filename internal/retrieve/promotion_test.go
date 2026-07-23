package retrieve

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// insMishapAt writes a raw agent.mishap event whose payload references the
// given memory ids -- the shape internal/mcp's session_end handler records.
func insMishapAt(t *testing.T, db *sql.DB, project string, ts time.Time, itemIDs ...string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"description": "test mishap", "item_ids": itemIDs})
	require.NoError(t, err)
	id, err := core.NewID()
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `
		INSERT INTO events (id, ts, kind, session_id, project_slug, item_id, payload)
		VALUES (?, ?, ?, '', ?, '', ?)`,
		id, core.FormatTime(ts.UTC()), string(core.EventAgentMishap), project, string(payload))
	require.NoError(t, err)
}

// A constraint referenced by a recent mishap claims the full tier ahead of a
// fresher constraint the blended (recency) order would have put first; the
// subagent briefing shares the same order.
func TestConstraintMishapPromotionBeatsBlendedRank(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01MIS", "constraint", "violated-rule", "hit last hour", "p", now.Add(-10*24*time.Hour))
	insMemAt(t, db, "01FRE", "constraint", "fresh-rule", "just written", "p", now)
	insMishapAt(t, db, "p", now.Add(-time.Hour), "01MIS")

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 1 }))

	b, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Equal(t, 1, fullConstraintLines(b))
	require.Contains(t, b, "CONSTRAINT: violated-rule: hit last hour")
	require.Contains(t, b, "Also binding (1): fresh-rule -- memory_read a name before working near it.")
	require.Subset(t, ids, []string{"01MIS", "01FRE"})

	// Subagent parity: rankConstraints runs before the subagent branch, so the
	// promoted constraint holds the full tier there too.
	sb, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", AgentType: "Explore"})
	require.NoError(t, err)
	require.Contains(t, sb, "CONSTRAINT: violated-rule: hit last hour")
	require.Contains(t, sb, "Also binding (1): fresh-rule -- memory_read a name before working near it.")
}

// Among promoted constraints the most recently violated ranks first, even when
// the blended recency order says the opposite; past the tier boundary the
// least-recently-violated promoted constraint is the one that drops to the
// compact line.
func TestConstraintMishapPromotionMostRecentFirst(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	// Updated stamps are the REVERSE of the mishap stamps, so blended recency
	// would order old-hit before mid-hit before new-hit; the mishap timestamps
	// must win instead.
	insMemAt(t, db, "01OLD", "constraint", "old-hit", "mishap five days ago", "p", now.Add(-1*time.Hour))
	insMemAt(t, db, "01MID", "constraint", "mid-hit", "mishap two days ago", "p", now.Add(-2*time.Hour))
	insMemAt(t, db, "01NEW", "constraint", "new-hit", "mishap one hour ago", "p", now.Add(-3*time.Hour))
	insMishapAt(t, db, "p", now.Add(-5*24*time.Hour), "01OLD")
	insMishapAt(t, db, "p", now.Add(-2*24*time.Hour), "01MID")
	insMishapAt(t, db, "p", now.Add(-time.Hour), "01NEW")

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 3 }))

	b, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Equal(t, 3, fullConstraintLines(b))
	iNew := strings.Index(b, "CONSTRAINT: new-hit")
	iMid := strings.Index(b, "CONSTRAINT: mid-hit")
	iOld := strings.Index(b, "CONSTRAINT: old-hit")
	require.True(t, iNew >= 0 && iMid >= 0 && iOld >= 0)
	require.True(t, iNew < iMid && iMid < iOld, "most recent mishap first: %s", b)

	// With fewer slots than promoted constraints, the least recent drops out.
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 2 }))
	b, _, err = svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Contains(t, b, "CONSTRAINT: new-hit")
	require.Contains(t, b, "CONSTRAINT: mid-hit")
	require.Contains(t, b, "Also binding (1): old-hit -- memory_read a name before working near it.")
}

// The promotion window is mishapPinWindowDays: a 31-day-old mishap no longer
// promotes, while a 29-day-old one still does.
func TestConstraintMishapPromotionWindowExpiry(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01EXP", "constraint", "expired-hit", "mishap 31 days ago", "p", now.Add(-3*time.Hour))
	insMemAt(t, db, "01REC", "constraint", "recent-hit", "mishap 29 days ago", "p", now.Add(-2*time.Hour))
	insMemAt(t, db, "01FRE", "constraint", "fresh-rule", "just written", "p", now)
	insMishapAt(t, db, "p", now.Add(-31*24*time.Hour), "01EXP")
	insMishapAt(t, db, "p", now.Add(-29*24*time.Hour), "01REC")

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 1 }))

	b, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Equal(t, 1, fullConstraintLines(b))
	require.Contains(t, b, "CONSTRAINT: recent-hit")
	// The expired mishap earns no promotion: the compact tier keeps the
	// blended (recency) order, fresh-rule ahead of expired-hit.
	require.Contains(t, b, "Also binding (2): fresh-rule, expired-hit -- memory_read a name before working near it.")
}

// A star is an explicit owner signal and outranks the implicit mishap signal:
// full-tier order is starred, then mishap-promoted, then blended -- and a
// mishap referencing the starred constraint itself changes nothing.
func TestConstraintMishapPromotionStarOutranks(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01STA", "constraint", "starred-rule", "old but starred", "p", now.Add(-30*24*time.Hour))
	insMemAt(t, db, "01MIS", "constraint", "violated-rule", "hit yesterday", "p", now.Add(-10*24*time.Hour))
	insMemAt(t, db, "01FRE", "constraint", "fresh-rule", "just written", "p", now)
	_, err := db.ExecContext(ctx, `UPDATE memories_index SET favorite = 1 WHERE id = '01STA'`)
	require.NoError(t, err)
	insMishapAt(t, db, "p", now.Add(-24*time.Hour), "01MIS")

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 3 }))

	b, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	iSta := strings.Index(b, "CONSTRAINT: starred-rule")
	iMis := strings.Index(b, "CONSTRAINT: violated-rule")
	iFre := strings.Index(b, "CONSTRAINT: fresh-rule")
	require.True(t, iSta >= 0 && iMis >= 0 && iFre >= 0)
	require.True(t, iSta < iMis && iMis < iFre, "starred, then mishap-promoted, then blended: %s", b)

	// One slot: the star takes it, and the compact tier keeps the class order
	// (mishap-promoted ahead of blended).
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 1 }))
	b, _, err = svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Equal(t, 1, fullConstraintLines(b))
	require.Contains(t, b, "CONSTRAINT: starred-rule")
	require.Contains(t, b, "Also binding (2): violated-rule, fresh-rule -- memory_read a name before working near it.")

	// A mishap against the starred constraint leaves it in the favorites
	// class -- the explicit signal already outranks the implicit one.
	insMishapAt(t, db, "p", now.Add(-time.Hour), "01STA")
	b, _, err = svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Equal(t, 1, fullConstraintLines(b))
	require.Contains(t, b, "CONSTRAINT: starred-rule")
	require.Contains(t, b, "Also binding (2): violated-rule, fresh-rule -- memory_read a name before working near it.")
}

// An unreadable mishap query costs the promotion, never the briefing (the
// utility-read posture): with the events table gone the briefing still renders
// every constraint, tiered by the blended order alone.
func TestConstraintMishapPromotionFailureSoft(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01MIS", "constraint", "violated-rule", "hit yesterday", "p", now.Add(-10*24*time.Hour))
	insMemAt(t, db, "01FRE", "constraint", "fresh-rule", "just written", "p", now)
	insMishapAt(t, db, "p", now.Add(-time.Hour), "01MIS")
	// Break exactly the mishap read: nothing else on the briefing path queries
	// the events table (utility activation reads settings, scores read the
	// stats projection).
	_, err := db.ExecContext(ctx, `DROP TABLE events`)
	require.NoError(t, err)

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 1 }))

	b, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Equal(t, 1, fullConstraintLines(b))
	// No promotion: the blended (recency) order decides the tier boundary.
	require.Contains(t, b, "CONSTRAINT: fresh-rule")
	require.Contains(t, b, "Also binding (1): violated-rule -- memory_read a name before working near it.")
	require.Subset(t, ids, []string{"01MIS", "01FRE"})
}

// Tiering disabled (constraint_max_full=0) keeps the legacy rendering exactly:
// promotion is part of the tier ranking only, so a recent mishap moves nothing
// and the order stays updated_at DESC.
func TestConstraintMishapPromotionTieringDisabled(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	now := time.Now()
	insMemAt(t, db, "01MIS", "constraint", "violated-rule", "hit yesterday", "p", now.Add(-10*24*time.Hour))
	insMemAt(t, db, "01FRA", "constraint", "fresh-a", "fresh a", "p", now.Add(-1*time.Hour))
	insMemAt(t, db, "01FRB", "constraint", "fresh-b", "fresh b", "p", now.Add(-2*time.Hour))
	insMishapAt(t, db, "p", now.Add(-time.Hour), "01MIS")

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.ConstraintMaxFull = 0 }))

	b, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Equal(t, 3, fullConstraintLines(b))
	require.NotContains(t, b, "Also binding")
	iA := strings.Index(b, "CONSTRAINT: fresh-a")
	iB := strings.Index(b, "CONSTRAINT: fresh-b")
	iM := strings.Index(b, "CONSTRAINT: violated-rule")
	require.True(t, iA >= 0 && iB >= 0 && iM >= 0)
	require.True(t, iA < iB && iB < iM, "legacy order is updated_at DESC, mishap not promoted: %s", b)
}
