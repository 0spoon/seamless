package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// NavCounts are the cheap roll-up counts the console shows in its sidebar. It is
// a handful of COUNT queries, safe to run on every page load.
type NavCounts struct {
	Sessions         int // all sessions
	Memories         int // active memories
	Notes            int // all notes
	OpenTasks        int // open or in_progress
	PendingProposals int // pending gardener proposals
}

// GetNavCounts computes the sidebar counts.
func GetNavCounts(ctx context.Context, db *sql.DB) (NavCounts, error) {
	var n NavCounts
	scalar := func(dest *int, query string) error {
		if err := db.QueryRowContext(ctx, query).Scan(dest); err != nil {
			return fmt.Errorf("store.GetNavCounts: %w", err)
		}
		return nil
	}
	if err := scalar(&n.Sessions, `SELECT COUNT(*) FROM sessions`); err != nil {
		return n, err
	}
	if err := scalar(&n.Memories, `SELECT COUNT(*) FROM memories_index WHERE invalid_at IS NULL`); err != nil {
		return n, err
	}
	if err := scalar(&n.Notes, `SELECT COUNT(*) FROM notes_index`); err != nil {
		return n, err
	}
	if err := scalar(&n.OpenTasks, `SELECT COUNT(*) FROM tasks WHERE status IN ('open','in_progress')`); err != nil {
		return n, err
	}
	if err := scalar(&n.PendingProposals, `SELECT COUNT(*) FROM gardener_proposals WHERE status = 'pending'`); err != nil {
		return n, err
	}
	return n, nil
}

// ProjectCounts are the per-project totals the console project peek shows. The
// channels do not overlap in the way coverage does; each is a plain count.
type ProjectCounts struct {
	Memories  int `json:"memories"`  // active memories in the project
	Sessions  int `json:"sessions"`  // sessions scoped to the project
	OpenTasks int `json:"openTasks"` // open or in_progress tasks
	Notes     int `json:"notes"`     // notes in the project
}

// GetProjectCounts computes the per-project roll-up for one slug in a single
// round trip (scalar subqueries). It backs the console project peek.
func GetProjectCounts(ctx context.Context, db *sql.DB, slug string) (ProjectCounts, error) {
	var c ProjectCounts
	err := db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM memories_index WHERE project = ? AND invalid_at IS NULL),
			(SELECT COUNT(*) FROM sessions WHERE project_slug = ?),
			(SELECT COUNT(*) FROM tasks WHERE project_slug = ? AND status IN ('open','in_progress')),
			(SELECT COUNT(*) FROM notes_index WHERE project = ?)`,
		slug, slug, slug, slug).
		Scan(&c.Memories, &c.Sessions, &c.OpenTasks, &c.Notes)
	if err != nil {
		return c, fmt.Errorf("store.GetProjectCounts: %w", err)
	}
	return c, nil
}

// KindRetrieval aggregates injection/read counts for one memory kind, backing
// the Retrieval page's per-kind table.
type KindRetrieval struct {
	Kind    string `json:"kind"`
	Injects int    `json:"injects"`
	Reads   int    `json:"reads"`
}

// RetrievalByKind returns per-kind injection/read totals over active memories
// that have a retrieval_stats row, highest injections first.
func RetrievalByKind(ctx context.Context, db *sql.DB) ([]KindRetrieval, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT m.kind, COALESCE(SUM(rs.inject_count), 0), COALESCE(SUM(rs.read_count), 0)
		FROM memories_index m
		JOIN retrieval_stats rs ON rs.item_id = m.id
		WHERE m.invalid_at IS NULL
		GROUP BY m.kind
		ORDER BY SUM(rs.inject_count) DESC, m.kind ASC`)
	if err != nil {
		return nil, fmt.Errorf("store.RetrievalByKind: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []KindRetrieval
	for rows.Next() {
		var k KindRetrieval
		if err := rows.Scan(&k.Kind, &k.Injects, &k.Reads); err != nil {
			return nil, fmt.Errorf("store.RetrievalByKind: scan: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// MemoryStat is an active memory annotated with its retrieval counts, for the
// Retrieval page's top-injected and stale lists.
type MemoryStat struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Kind         string     `json:"kind"`
	Project      string     `json:"project"`
	Injects      int        `json:"injects"`
	Reads        int        `json:"reads"`
	Updated      time.Time  `json:"updated"`
	LastInjected *time.Time `json:"lastInjected,omitempty"`
}

// TopInjectedMemories returns the most-injected active memories, highest first.
func TopInjectedMemories(ctx context.Context, db *sql.DB, limit int) ([]MemoryStat, error) {
	if limit <= 0 {
		limit = 12
	}
	rows, err := db.QueryContext(ctx, `
		SELECT m.id, m.name, m.kind, m.project, rs.inject_count, rs.read_count, m.updated_at, rs.last_injected_at
		FROM retrieval_stats rs
		JOIN memories_index m ON m.id = rs.item_id AND m.invalid_at IS NULL
		WHERE rs.inject_count > 0
		ORDER BY rs.inject_count DESC, m.id ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store.TopInjectedMemories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanMemoryStats(rows)
}

func scanMemoryStats(rows *sql.Rows) ([]MemoryStat, error) {
	var out []MemoryStat
	for rows.Next() {
		var (
			m            MemoryStat
			updated      string
			lastInjected sql.NullString
		)
		if err := rows.Scan(&m.ID, &m.Name, &m.Kind, &m.Project, &m.Injects, &m.Reads, &updated, &lastInjected); err != nil {
			return nil, fmt.Errorf("store: scan memory stat: %w", err)
		}
		var err error
		if m.Updated, err = core.ParseTime(updated); err != nil {
			return nil, fmt.Errorf("store: memory stat updated_at: %w", err)
		}
		if m.LastInjected, err = nullTimePtr(lastInjected); err != nil {
			return nil, fmt.Errorf("store: memory stat last_injected_at: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SessionCoverage measures how much Claude Code session knowledge Seamless is
// retaining: a session is "covered" when it left a durable artifact behind --
// non-empty findings, or at least one written memory, note, or recorded trial.
// It is a rough proxy for "how much of what happened in a session did we keep".
// The per-channel counts (Findings/Memories/Notes/Trials) overlap -- a session
// can be covered several ways -- so only Total and Covered partition the set.
type SessionCoverage struct {
	Total    int `json:"total"`    // all sessions (the denominator)
	Covered  int `json:"covered"`  // sessions with >=1 durable artifact
	Findings int `json:"findings"` // sessions whose findings are non-empty
	Memories int `json:"memories"` // sessions that wrote >=1 memory
	Notes    int `json:"notes"`    // sessions that created >=1 note
	Trials   int `json:"trials"`   // sessions that recorded >=1 trial
}

// GetSessionCoverage computes the coverage roll-up in a single pass over
// sessions, testing each against the event log for durable artifacts. It reads
// the event log directly rather than retrieval_stats, so it needs no rebuild.
func GetSessionCoverage(ctx context.Context, db *sql.DB) (SessionCoverage, error) {
	var c SessionCoverage
	err := db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN has_findings OR has_mem OR has_note OR has_trial THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(has_findings), 0),
			COALESCE(SUM(has_mem), 0),
			COALESCE(SUM(has_note), 0),
			COALESCE(SUM(has_trial), 0)
		FROM (
			SELECT
				(s.findings <> '') AS has_findings,
				EXISTS (SELECT 1 FROM events e WHERE e.session_id = s.id AND e.kind = ?) AS has_mem,
				EXISTS (SELECT 1 FROM events e WHERE e.session_id = s.id AND e.kind = ?) AS has_note,
				EXISTS (SELECT 1 FROM events e WHERE e.session_id = s.id AND e.kind = ?) AS has_trial
			FROM sessions s
		)`,
		string(core.EventMemoryWritten), string(core.EventNoteWritten), string(core.EventTrialRecorded),
	).Scan(&c.Total, &c.Covered, &c.Findings, &c.Memories, &c.Notes, &c.Trials)
	if err != nil {
		return c, fmt.Errorf("store.GetSessionCoverage: %w", err)
	}
	return c, nil
}

// DayCoverage is one calendar day's session-coverage roll-up, for the coverage
// trend chart: of the sessions created that day (UTC), how many left a durable
// artifact behind. Total == 0 means no sessions started that day, so that day's
// coverage is undefined (the caller renders it as a gap, not 0%).
type DayCoverage struct {
	Day     string `json:"day"`     // YYYY-MM-DD (UTC)
	Total   int    `json:"total"`   // sessions created that day
	Covered int    `json:"covered"` // of those, how many retained knowledge
}

// SessionCoverageByDay buckets sessions by their creation day (UTC) over the
// trailing `days` days and, per day, counts how many were "covered" -- left a
// durable artifact behind (non-empty findings, or a written memory/note/recorded
// trial). It applies the same covered-ness test as GetSessionCoverage, sliced by
// day, to back the overview's coverage-trend chart. Days with no sessions are
// omitted (the caller densifies the window).
func SessionCoverageByDay(ctx context.Context, db *sql.DB, days int) ([]DayCoverage, error) {
	if days <= 0 {
		days = 14
	}
	since := core.FormatTime(time.Now().UTC().AddDate(0, 0, -days))
	rows, err := db.QueryContext(ctx, `
		SELECT
			substr(s.created_at, 1, 10) AS day,
			COUNT(*),
			COALESCE(SUM(CASE WHEN
				s.findings <> ''
				OR EXISTS (SELECT 1 FROM events e WHERE e.session_id = s.id AND e.kind = ?)
				OR EXISTS (SELECT 1 FROM events e WHERE e.session_id = s.id AND e.kind = ?)
				OR EXISTS (SELECT 1 FROM events e WHERE e.session_id = s.id AND e.kind = ?)
			THEN 1 ELSE 0 END), 0)
		FROM sessions s
		WHERE s.created_at >= ?
		GROUP BY day ORDER BY day ASC`,
		string(core.EventMemoryWritten), string(core.EventNoteWritten), string(core.EventTrialRecorded), since)
	if err != nil {
		return nil, fmt.Errorf("store.SessionCoverageByDay: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DayCoverage
	for rows.Next() {
		var d DayCoverage
		if err := rows.Scan(&d.Day, &d.Total, &d.Covered); err != nil {
			return nil, fmt.Errorf("store.SessionCoverageByDay: scan: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DayCount is one calendar day's event count, for the injection trend.
type DayCount struct {
	Day   string `json:"day"` // YYYY-MM-DD
	Count int    `json:"count"`
}

// InjectionsByDay returns the count of retrieval.injected events per calendar day
// over the trailing `days` days (UTC), oldest day first. Days with no injections
// are omitted (the caller fills gaps if it wants a dense series).
func InjectionsByDay(ctx context.Context, db *sql.DB, days int) ([]DayCount, error) {
	if days <= 0 {
		days = 14
	}
	since := core.FormatTime(time.Now().UTC().AddDate(0, 0, -days))
	rows, err := db.QueryContext(ctx, `
		SELECT substr(ts, 1, 10) AS day, COUNT(*)
		FROM events
		WHERE kind = ? AND ts >= ?
		GROUP BY day ORDER BY day ASC`, string(core.EventInjected), since)
	if err != nil {
		return nil, fmt.Errorf("store.InjectionsByDay: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DayCount
	for rows.Next() {
		var d DayCount
		if err := rows.Scan(&d.Day, &d.Count); err != nil {
			return nil, fmt.Errorf("store.InjectionsByDay: scan: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
