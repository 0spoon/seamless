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
	if err := scalar(&n.OpenTasks, `SELECT COUNT(*) FROM tasks WHERE status IN ('open','in_progress')`); err != nil {
		return n, err
	}
	if err := scalar(&n.PendingProposals, `SELECT COUNT(*) FROM gardener_proposals WHERE status = 'pending'`); err != nil {
		return n, err
	}
	return n, nil
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
