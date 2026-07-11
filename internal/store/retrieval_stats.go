package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// RetrievalStat mirrors one retrieval_stats row: how often an item has been
// surfaced (injected) to an agent and read back, and when last. It is a
// materialized projection of the append-only event log, rebuilt by
// RebuildRetrievalStats -- the events table is the source of truth.
type RetrievalStat struct {
	ItemID         string
	InjectCount    int
	ReadCount      int
	LastInjectedAt *time.Time
	LastReadAt     *time.Time
}

// injectedPayload is the shape RebuildRetrievalStats reads out of a
// retrieval.injected event's payload: recall records the surfaced item ids here
// (the item_id column is reserved for single-item events like memory.read).
type injectedPayload struct {
	ItemIDs []string `json:"item_ids"`
}

// RebuildRetrievalStats recomputes the entire retrieval_stats table from the
// event log. It is the canonical maintenance path (the gardener calls it at the
// top of each pass, and it is safe to call after an import): retrieval.injected
// events contribute inject_count + last_injected_at (per item id, whether the id
// is in the item_id column or the payload's item_ids array), and memory.read
// events contribute read_count + last_read_at. Events are read oldest-first so
// the last-seen timestamp wins.
func RebuildRetrievalStats(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
		SELECT ts, kind, item_id, payload FROM events
		WHERE kind IN (?, ?)
		ORDER BY ts ASC, id ASC`,
		string(core.EventInjected), string(core.EventMemoryRead))
	if err != nil {
		return fmt.Errorf("store.RebuildRetrievalStats: query events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	stats := make(map[string]*RetrievalStat)
	stat := func(id string) *RetrievalStat {
		s := stats[id]
		if s == nil {
			s = &RetrievalStat{ItemID: id}
			stats[id] = s
		}
		return s
	}

	for rows.Next() {
		var tsStr, kind, itemID, payload string
		if err := rows.Scan(&tsStr, &kind, &itemID, &payload); err != nil {
			return fmt.Errorf("store.RebuildRetrievalStats: scan: %w", err)
		}
		ts, err := core.ParseTime(tsStr)
		if err != nil {
			return fmt.Errorf("store.RebuildRetrievalStats: parse ts: %w", err)
		}
		switch core.EventKind(kind) {
		case core.EventInjected:
			for _, id := range injectedItemIDs(itemID, payload) {
				s := stat(id)
				s.InjectCount++
				t := ts
				s.LastInjectedAt = &t
			}
		case core.EventMemoryRead:
			if itemID == "" {
				continue
			}
			s := stat(itemID)
			s.ReadCount++
			t := ts
			s.LastReadAt = &t
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store.RebuildRetrievalStats: rows: %w", err)
	}

	return replaceRetrievalStats(ctx, db, stats)
}

// injectedItemIDs returns every item id an injection event refers to: the
// item_id column (when set) plus the payload's item_ids array (recall's form).
func injectedItemIDs(itemID, payload string) []string {
	var ids []string
	if itemID != "" {
		ids = append(ids, itemID)
	}
	if payload != "" && payload != "{}" {
		var p injectedPayload
		if err := json.Unmarshal([]byte(payload), &p); err == nil {
			for _, id := range p.ItemIDs {
				if id != "" {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

// replaceRetrievalStats atomically swaps the retrieval_stats table for the given
// aggregates (clear + bulk insert in one transaction).
func replaceRetrievalStats(ctx context.Context, db *sql.DB, stats map[string]*RetrievalStat) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store.RebuildRetrievalStats: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM retrieval_stats`); err != nil {
		return fmt.Errorf("store.RebuildRetrievalStats: clear: %w", err)
	}
	for _, s := range stats {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO retrieval_stats (item_id, inject_count, read_count, last_injected_at, last_read_at)
			VALUES (?, ?, ?, ?, ?)`,
			s.ItemID, s.InjectCount, s.ReadCount,
			formatTimePtr(s.LastInjectedAt), formatTimePtr(s.LastReadAt)); err != nil {
			return fmt.Errorf("store.RebuildRetrievalStats: insert %s: %w", s.ItemID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store.RebuildRetrievalStats: commit: %w", err)
	}
	return nil
}

// GetRetrievalStat returns the stats row for an item. found is false when the
// item has never been injected or read.
func GetRetrievalStat(ctx context.Context, db *sql.DB, itemID string) (RetrievalStat, bool, error) {
	var (
		s                      RetrievalStat
		lastInjected, lastRead sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT item_id, inject_count, read_count, last_injected_at, last_read_at
		FROM retrieval_stats WHERE item_id = ?`, itemID).
		Scan(&s.ItemID, &s.InjectCount, &s.ReadCount, &lastInjected, &lastRead)
	if errors.Is(err, sql.ErrNoRows) {
		return RetrievalStat{}, false, nil
	}
	if err != nil {
		return RetrievalStat{}, false, fmt.Errorf("store.GetRetrievalStat: %w", err)
	}
	var perr error
	if s.LastInjectedAt, perr = nullTimePtr(lastInjected); perr != nil {
		return RetrievalStat{}, false, fmt.Errorf("store.GetRetrievalStat: last_injected_at: %w", perr)
	}
	if s.LastReadAt, perr = nullTimePtr(lastRead); perr != nil {
		return RetrievalStat{}, false, fmt.Errorf("store.GetRetrievalStat: last_read_at: %w", perr)
	}
	return s, true, nil
}

// AllRetrievalStats loads the whole retrieval_stats table into a map keyed by
// item id, so a caller (the console) can annotate many memories with one query
// instead of N GetRetrievalStat calls.
func AllRetrievalStats(ctx context.Context, db *sql.DB) (map[string]RetrievalStat, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT item_id, inject_count, read_count, last_injected_at, last_read_at
		FROM retrieval_stats`)
	if err != nil {
		return nil, fmt.Errorf("store.AllRetrievalStats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]RetrievalStat)
	for rows.Next() {
		var (
			s                      RetrievalStat
			lastInjected, lastRead sql.NullString
		)
		if err := rows.Scan(&s.ItemID, &s.InjectCount, &s.ReadCount, &lastInjected, &lastRead); err != nil {
			return nil, fmt.Errorf("store.AllRetrievalStats: scan: %w", err)
		}
		var perr error
		if s.LastInjectedAt, perr = nullTimePtr(lastInjected); perr != nil {
			return nil, fmt.Errorf("store.AllRetrievalStats: last_injected_at: %w", perr)
		}
		if s.LastReadAt, perr = nullTimePtr(lastRead); perr != nil {
			return nil, fmt.Errorf("store.AllRetrievalStats: last_read_at: %w", perr)
		}
		out[s.ItemID] = s
	}
	return out, rows.Err()
}

// StaleMemories returns active memories that have seen no activity since cutoff:
// neither updated, injected, nor read on or after that instant. It LEFT JOINs
// retrieval_stats (a memory with no stats row counts as never injected/read, so
// only its updated_at matters), oldest-updated first. It backs the gardener's
// staleness pass.
func StaleMemories(ctx context.Context, db *sql.DB, cutoff time.Time) ([]core.Memory, error) {
	c := core.FormatTime(cutoff.UTC())
	rows, err := db.QueryContext(ctx, `SELECT `+memoryCols+`
		FROM memories_index
		LEFT JOIN retrieval_stats ON retrieval_stats.item_id = memories_index.id
		WHERE invalid_at IS NULL
		  AND updated_at < ?
		  AND (last_injected_at IS NULL OR last_injected_at < ?)
		  AND (last_read_at IS NULL OR last_read_at < ?)
		ORDER BY updated_at ASC, id ASC`, c, c, c)
	if err != nil {
		return nil, fmt.Errorf("store.StaleMemories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	mems, err := scanMemories(rows)
	if err != nil {
		return nil, fmt.Errorf("store.StaleMemories: %w", err)
	}
	return mems, nil
}

// formatTimePtr renders a nullable timestamp for a nullable column: nil -> NULL.
func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return core.FormatTime(t.UTC())
}
