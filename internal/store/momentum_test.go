package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// coveredSessionOn seeds a session created at noon (local) of the day `offset`
// days from now, covered (findings) or not.
func coveredSessionOn(t *testing.T, db *sql.DB, now time.Time, offset int, covered bool) {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	at := localDay(now).AddDate(0, 0, offset).Add(12 * time.Hour)
	s := core.Session{
		ID: id, Name: "cc/" + id, Status: core.SessionCompleted,
		CreatedAt: at, UpdatedAt: at,
	}
	if covered {
		s.Findings = "left something durable"
	}
	require.NoError(t, CreateSession(context.Background(), db, s))
}

func TestCaptureStreak_EmptyDatabase(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	current, longest, err := CaptureStreak(ctx, db, time.Now())
	require.NoError(t, err)
	require.Zero(t, current)
	require.Zero(t, longest)

	_, found, err := GetSetting(ctx, db, SettingCaptureStreak)
	require.NoError(t, err)
	require.False(t, found, "nothing to finalize means nothing stored")
}

func TestCaptureStreak_RunsGapsAndTodayPause(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now()

	// Four covered days, a gap, then two covered days ending yesterday. An
	// uncovered session on the gap day must not bridge it: the streak counts
	// covered days, not active ones.
	for _, off := range []int{-9, -8, -7, -6, -2, -1} {
		coveredSessionOn(t, db, now, off, true)
	}
	coveredSessionOn(t, db, now, -4, false)

	current, longest, err := CaptureStreak(ctx, db, now)
	require.NoError(t, err)
	require.Equal(t, 2, current, "today is still pending: the run through yesterday stands")
	require.Equal(t, 4, longest)

	// Today covered extends the current run immediately (computed live).
	coveredSessionOn(t, db, now, 0, true)
	current, longest, err = CaptureStreak(ctx, db, now)
	require.NoError(t, err)
	require.Equal(t, 3, current)
	require.Equal(t, 4, longest)
}

func TestCaptureStreak_CurrentCanBeTheLongest(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now()

	for _, off := range []int{-2, -1, 0} {
		coveredSessionOn(t, db, now, off, true)
	}
	current, longest, err := CaptureStreak(ctx, db, now)
	require.NoError(t, err)
	require.Equal(t, 3, current)
	require.Equal(t, 3, longest, "a live run counts toward longest without waiting to be finalized")
}

func TestCaptureStreak_CacheAdvancesIncrementally(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now()

	coveredSessionOn(t, db, now, -1, true)
	current, longest, err := CaptureStreak(ctx, db, now)
	require.NoError(t, err)
	require.Equal(t, 1, current)
	require.Equal(t, 1, longest)

	raw, found, err := GetSetting(ctx, db, SettingCaptureStreak)
	require.NoError(t, err)
	require.True(t, found)
	require.Contains(t, raw, `"through":"`+bucketKey(localDay(now).AddDate(0, 0, -1), false)+`"`,
		"the cache finalizes through yesterday, never today")

	// Two days later, with both in-between days covered: the finalize step
	// walks only the days after the watermark, and the cached run carries
	// across calls into one continuous streak.
	later := now.AddDate(0, 0, 2)
	coveredSessionOn(t, db, now, 0, true)
	coveredSessionOn(t, db, now, 1, true)
	current, longest, err = CaptureStreak(ctx, db, later)
	require.NoError(t, err)
	require.Equal(t, 3, current, "yesterday's run stands while `later`'s own day is pending")
	require.Equal(t, 3, longest)

	raw, found, err = GetSetting(ctx, db, SettingCaptureStreak)
	require.NoError(t, err)
	require.True(t, found)
	require.Contains(t, raw, `"through":"`+bucketKey(localDay(later).AddDate(0, 0, -1), false)+`"`)
	require.Contains(t, raw, `"run":3`)
}

func TestCaptureStreak_CorruptCacheRebuilds(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now()

	for _, off := range []int{-3, -2, -1} {
		coveredSessionOn(t, db, now, off, true)
	}
	require.NoError(t, SetSetting(ctx, db, SettingCaptureStreak, "not json at all"))

	current, longest, err := CaptureStreak(ctx, db, now)
	require.NoError(t, err)
	require.Equal(t, 3, current)
	require.Equal(t, 3, longest)
}

// insertReadEvent writes a memory.read event carrying a session id, which the
// spotlight needs for per-session dedup and its readers count.
func insertReadEvent(t *testing.T, db *sql.DB, itemID, sessionID string, ts time.Time) {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `
		INSERT INTO events (id, ts, kind, session_id, project_slug, item_id, payload)
		VALUES (?, ?, ?, ?, '', ?, '{}')`,
		id, core.FormatTime(ts), string(core.EventMemoryRead), sessionID, itemID)
	require.NoError(t, err)
}

func TestMemorySpotlight(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	since := now.AddDate(0, 0, -30)

	seedMemoryRow(t, db, "A", "chroma-boot-race", now)
	seedMemoryRow(t, db, "B", "quiet-memory", now)
	seedMemoryRow(t, db, "C", "retired-star", now)
	_, err := db.ExecContext(ctx, `UPDATE memories_index SET invalid_at = ? WHERE id = 'C'`,
		core.FormatTime(now))
	require.NoError(t, err)

	// A: read by three sessions in the window, one of them twice -- the repeat
	// bumps the read count but not the deduped utility.
	insertReadEvent(t, db, "A", "s1", now.Add(-time.Hour))
	insertReadEvent(t, db, "A", "s1", now.Add(-2*time.Hour))
	insertReadEvent(t, db, "A", "s2", now.Add(-24*time.Hour))
	insertReadEvent(t, db, "A", "s3", now.Add(-48*time.Hour))
	// B: one read in the window.
	insertReadEvent(t, db, "B", "s1", now.Add(-time.Hour))
	// C (retired) out-reads them all but cannot be spotlighted.
	for i, sess := range []string{"s1", "s2", "s3", "s4", "s5"} {
		insertReadEvent(t, db, "C", sess, now.Add(-time.Duration(i+1)*time.Hour))
	}
	// A read far outside the window earns nothing.
	insertReadEvent(t, db, "B", "s9", now.AddDate(0, 0, -40))

	got, found, err := MemorySpotlight(ctx, db, since, now)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "A", got.ItemID)
	require.Equal(t, "chroma-boot-race", got.Name)
	require.Equal(t, "gotcha", got.Kind)
	require.Equal(t, 4, got.Reads)
	require.Equal(t, 3, got.Readers)
	require.Positive(t, got.Utility)

	// The honest empty state: a window with no qualifying activity.
	empty, found, err := MemorySpotlight(ctx, db, now.AddDate(0, 0, 1), now.AddDate(0, 0, 2))
	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, empty.ItemID)
}

// PlansShippedSince is the judged source behind the Plans page's monthly
// count and settle marks: only plan.shipped rows, only at or after since,
// project and slug verbatim from the settlement event.
func TestPlansShippedSince_WindowsBySettlementTime(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	monthStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	insertProjectEvent(t, db, core.EventPlanShipped, "", "demo", "old-plan", "{}", monthStart.Add(-time.Hour))
	insertProjectEvent(t, db, core.EventPlanShipped, "", "demo", "fresh-plan", "{}", monthStart.Add(time.Hour))
	insertProjectEvent(t, db, core.EventPlanShipped, "", "other", "elsewhere", "{}", monthStart.Add(2*time.Hour))
	insertProjectEvent(t, db, core.EventTaskTransition, "", "demo", "not-a-settlement", "{}", monthStart.Add(time.Hour))

	refs, err := PlansShippedSince(ctx, db, monthStart)
	require.NoError(t, err)
	require.Equal(t, []PlanShippedRef{
		{Project: "demo", Slug: "fresh-plan"},
		{Project: "other", Slug: "elsewhere"},
	}, refs)

	// Nothing in the window is nothing, not an error.
	empty, err := PlansShippedSince(ctx, db, monthStart.AddDate(0, 1, 0))
	require.NoError(t, err)
	require.Empty(t, empty)
}
