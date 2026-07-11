package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// insertSessionEvent writes a raw event row tied to a session, for coverage
// tests (insertEvent hardcodes an empty session_id).
func insertSessionEvent(t *testing.T, db *sql.DB, kind core.EventKind, sessionID string, ts time.Time) {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `
		INSERT INTO events (id, ts, kind, session_id, project_slug, item_id, payload)
		VALUES (?, ?, ?, ?, '', '', '{}')`,
		id, core.FormatTime(ts), string(kind), sessionID)
	require.NoError(t, err)
}

func TestGetSessionCoverage(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mk := func(id, name string) core.Session {
		return core.Session{ID: id, Name: name, Status: core.SessionCompleted, CreatedAt: now, UpdatedAt: now}
	}

	// s1: covered by findings only.
	s1 := mk("s1", "cc/a")
	s1.Findings = "learned a thing"
	require.NoError(t, CreateSession(ctx, db, s1))
	// s2: covered by a written memory only.
	require.NoError(t, CreateSession(ctx, db, mk("s2", "cc/b")))
	insertSessionEvent(t, db, core.EventMemoryWritten, "s2", now)
	// s3: covered by a note only.
	require.NoError(t, CreateSession(ctx, db, mk("s3", "cc/c")))
	insertSessionEvent(t, db, core.EventNoteWritten, "s3", now)
	// s4: covered by a trial only.
	require.NoError(t, CreateSession(ctx, db, mk("s4", "cc/d")))
	insertSessionEvent(t, db, core.EventTrialRecorded, "s4", now)
	// s5: covered several ways (findings + memory) -- must not double-count.
	s5 := mk("s5", "cc/e")
	s5.Findings = "big session"
	require.NoError(t, CreateSession(ctx, db, s5))
	insertSessionEvent(t, db, core.EventMemoryWritten, "s5", now)
	// s6: uncovered -- only a read and an injection, no durable artifact.
	require.NoError(t, CreateSession(ctx, db, mk("s6", "cc/f")))
	insertSessionEvent(t, db, core.EventMemoryRead, "s6", now)
	insertSessionEvent(t, db, core.EventInjected, "s6", now)
	// s7: uncovered -- started and ended empty.
	require.NoError(t, CreateSession(ctx, db, mk("s7", "cc/g")))

	c, err := GetSessionCoverage(ctx, db)
	require.NoError(t, err)
	require.Equal(t, 7, c.Total)
	require.Equal(t, 5, c.Covered)  // s1..s5; s6,s7 uncovered
	require.Equal(t, 2, c.Findings) // s1, s5
	require.Equal(t, 2, c.Memories) // s2, s5
	require.Equal(t, 1, c.Notes)    // s3
	require.Equal(t, 1, c.Trials)   // s4
}

func TestGetSessionCoverage_Empty(t *testing.T) {
	db := openTestDB(t)
	c, err := GetSessionCoverage(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, SessionCoverage{}, c)
}

func TestSessionCoverageByDay(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	d0 := now                   // today
	d1 := now.AddDate(0, 0, -1) // yesterday

	mk := func(id, name string, created time.Time) core.Session {
		return core.Session{ID: id, Name: name, Status: core.SessionCompleted, CreatedAt: created, UpdatedAt: created}
	}

	// Today: one covered (findings), one uncovered.
	s1 := mk("d0a", "cc/d0a", d0)
	s1.Findings = "kept something"
	require.NoError(t, CreateSession(ctx, db, s1))
	require.NoError(t, CreateSession(ctx, db, mk("d0b", "cc/d0b", d0)))
	// Yesterday: one covered via a written memory.
	require.NoError(t, CreateSession(ctx, db, mk("d1a", "cc/d1a", d1)))
	insertSessionEvent(t, db, core.EventMemoryWritten, "d1a", now)

	got, err := SessionCoverageByDay(ctx, db, 14)
	require.NoError(t, err)
	require.Len(t, got, 2)

	byDay := map[string]DayCoverage{}
	for _, d := range got {
		byDay[d.Day] = d
	}
	require.Equal(t, DayCoverage{Day: d0.Format("2006-01-02"), Total: 2, Covered: 1}, byDay[d0.Format("2006-01-02")])
	require.Equal(t, DayCoverage{Day: d1.Format("2006-01-02"), Total: 1, Covered: 1}, byDay[d1.Format("2006-01-02")])
}

func TestSessionCoverageByDay_Empty(t *testing.T) {
	db := openTestDB(t)
	got, err := SessionCoverageByDay(context.Background(), db, 14)
	require.NoError(t, err)
	require.Empty(t, got)
}
