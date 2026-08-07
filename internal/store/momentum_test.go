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
