// Momentum: the capture streak behind the activity calendar.
//
// The streak counts covered local days -- days on which at least one session
// left a durable artifact (the same covered-ness test as SessionCoverage), so
// it rewards knowledge capture, not raw usage. Day boundaries follow the
// localBucketAxis conventions: local midnights advanced with AddDate, never
// 24h arithmetic, so DST transitions cannot split or merge a day.
//
// The longest-ever run is cached in a settings row and extended incrementally,
// finalized through yesterday; only the days since the cached watermark are
// ever queried, so a load never rewalks the full session history. Today is
// always computed live and never persisted: a day is finalized only once it is
// over, which also gives a session created late yesterday until the first load
// of today for its artifact to land. (An artifact landing two or more days
// after its session's creation day can still be missed by an already-finalized
// day -- accepted: the cache only ever underreports, and longest never
// regresses.)
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// SettingCaptureStreak is the settings key caching the momentum capture
// streak: {"longest":N,"run":R,"through":"2006-01-02"}, finalized through the
// named local day. Deleting the row is safe -- the next load rebuilds it from
// the session table.
const SettingCaptureStreak = "momentum_capture_streak"

// captureStreakCache is the stored shape behind SettingCaptureStreak.
type captureStreakCache struct {
	// Longest is the longest run of consecutive covered days ever finalized.
	Longest int `json:"longest"`
	// Run is the covered-day run length ending at Through (0 when Through was
	// not covered) -- what a still-growing streak continues from.
	Run int `json:"run"`
	// Through is the last finalized local day, as bucketKey formats it.
	Through string `json:"through"`
}

// CaptureStreak reports the momentum capture streak: the current run of
// consecutive covered local days and the longest run ever. Today counts into
// the current run as soon as it is covered; an uncovered today is a pause, not
// a break -- the run through yesterday stands until the day is actually over.
// A momentum-only surface: callers gate on the feature, so a disabled install
// neither queries nor writes the cache.
func CaptureStreak(ctx context.Context, db *sql.DB, now time.Time) (current, longest int, err error) {
	today := localDay(now)
	yesterday := today.AddDate(0, 0, -1)

	cache, found, err := captureStreakCached(ctx, db)
	if err != nil {
		return 0, 0, err
	}
	through, usable := parseDay(cache.Through)
	if !found || !usable || through.After(yesterday) {
		// No usable watermark: a fresh cache, a malformed row, or a clock that
		// moved backwards past it. Rebuild from the whole session table, once.
		cache = captureStreakCache{}
		through = time.Time{}
	}

	if !through.Equal(yesterday) {
		// Finalize the days after the watermark, through yesterday.
		var since time.Time // zero = all sessions (first build)
		if !through.IsZero() {
			since = through.AddDate(0, 0, 1)
		}
		covered, earliest, cerr := coveredDays(ctx, db, since)
		if cerr != nil {
			return 0, 0, cerr
		}
		start := since
		if start.IsZero() {
			if earliest.IsZero() {
				// No sessions at all: nothing to finalize, nothing to store.
				return 0, 0, nil
			}
			start = localDay(earliest)
		}
		for day := start; !day.After(yesterday); day = day.AddDate(0, 0, 1) {
			if covered[bucketKey(day, false)] {
				cache.Run++
				cache.Longest = max(cache.Longest, cache.Run)
			} else {
				cache.Run = 0
			}
		}
		cache.Through = bucketKey(yesterday, false)
		raw, merr := json.Marshal(cache)
		if merr != nil {
			return 0, 0, fmt.Errorf("store.CaptureStreak: encode cache: %w", merr)
		}
		if serr := SetSetting(ctx, db, SettingCaptureStreak, string(raw)); serr != nil {
			return 0, 0, serr
		}
	}

	// Today, live and unpersisted.
	todayCovered, _, err := coveredDays(ctx, db, today)
	if err != nil {
		return 0, 0, err
	}
	current = cache.Run
	if todayCovered[bucketKey(today, false)] {
		current++
	}
	return current, max(cache.Longest, current), nil
}

// captureStreakCached loads the cache row. A corrupt row reads as absent (the
// next finalize rebuilds and rewrites it) rather than failing the page.
func captureStreakCached(ctx context.Context, db *sql.DB) (captureStreakCache, bool, error) {
	var cache captureStreakCache
	raw, found, err := GetSetting(ctx, db, SettingCaptureStreak)
	if err != nil {
		return cache, false, fmt.Errorf("store.CaptureStreak: %w", err)
	}
	if !found {
		return cache, false, nil
	}
	if err := json.Unmarshal([]byte(raw), &cache); err != nil {
		return captureStreakCache{}, false, nil
	}
	return cache, true, nil
}

// coveredDays reports, per local day key, whether any session created since
// the given instant (zero = all time) is covered -- the SessionCoverage test:
// findings, a memory write, a note write, or a recorded trial. It also returns
// the earliest session creation time seen, for the first full build.
func coveredDays(ctx context.Context, db *sql.DB, since time.Time) (map[string]bool, time.Time, error) {
	args := []any{string(core.EventMemoryWritten), string(core.EventNoteWritten), string(core.EventTrialRecorded)}
	q := `
		SELECT
			s.created_at,
			CASE WHEN
				s.findings <> ''
				OR EXISTS (SELECT 1 FROM events e WHERE e.session_id = s.id AND e.kind = ?)
				OR EXISTS (SELECT 1 FROM events e WHERE e.session_id = s.id AND e.kind = ?)
				OR EXISTS (SELECT 1 FROM events e WHERE e.session_id = s.id AND e.kind = ?)
			THEN 1 ELSE 0 END AS covered
		FROM sessions s`
	if !since.IsZero() {
		q += ` WHERE s.created_at >= ?`
		args = append(args, core.FormatTime(since))
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("store.coveredDays: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	var earliest time.Time
	for rows.Next() {
		var createdAt string
		var covered int
		if err := rows.Scan(&createdAt, &covered); err != nil {
			return nil, time.Time{}, fmt.Errorf("store.coveredDays: scan: %w", err)
		}
		ts, err := core.ParseTime(createdAt)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("store.coveredDays: parse ts: %w", err)
		}
		if earliest.IsZero() || ts.Before(earliest) {
			earliest = ts
		}
		if covered == 1 {
			out[bucketKey(ts, false)] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, fmt.Errorf("store.coveredDays: %w", err)
	}
	return out, earliest, nil
}

// localDay floors t to its local midnight, the day-boundary convention every
// momentum surface shares with localBucketAxis.
func localDay(t time.Time) time.Time {
	l := t.Local()
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, l.Location())
}

// parseDay parses a bucketKey-formatted local day back to its local midnight.
// ok is false for an empty or malformed key.
func parseDay(key string) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01-02", key, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
