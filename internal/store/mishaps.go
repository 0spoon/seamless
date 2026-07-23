package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// mishapPayload is the slice of an agent.mishap event payload this package
// reads: the ids of memories the mishap's text named, matched once at
// ingestion (internal/mcp's session_end handler) and durable in the event log
// ever since -- nothing re-matches here.
type mishapPayload struct {
	ItemIDs []string `json:"item_ids"`
}

// RecentMishapItemIDs returns the ids of memories referenced by a project's
// agent.mishap events recorded within window of now, each mapped to the
// timestamp of its most recent referencing mishap. It feeds the briefing's
// mishap promotion -- an ordering signal like favorites -- and deliberately
// NOT the utility score: RebuildRetrievalStats keeps its query-gated classes
// (read/recall/prompt) per the closed-loop-utility-signal-contract. Mishaps
// whose payload names no memory contribute nothing; no references yields an
// empty map, not an error.
func RecentMishapItemIDs(ctx context.Context, db *sql.DB, project string, window time.Duration) (map[string]time.Time, error) {
	return recentMishapItemIDs(ctx, db, project, window, time.Now().UTC())
}

// recentMishapItemIDs is RecentMishapItemIDs with an injectable clock, so tests
// pin the window edge instead of sleeping. The cutoff comparison is a string
// >= on the fixed-width core.TimeFormat, which matches chronological order.
func recentMishapItemIDs(ctx context.Context, db *sql.DB, project string, window time.Duration, now time.Time) (map[string]time.Time, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT ts, payload FROM events
		WHERE kind = ? AND project_slug = ? AND ts >= ?
		ORDER BY ts ASC, id ASC`,
		string(core.EventAgentMishap), project, core.FormatTime(now.Add(-window)))
	if err != nil {
		return nil, fmt.Errorf("store.RecentMishapItemIDs: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]time.Time{}
	for rows.Next() {
		var tsStr, payload string
		if err := rows.Scan(&tsStr, &payload); err != nil {
			return nil, fmt.Errorf("store.RecentMishapItemIDs: scan: %w", err)
		}
		if payload == "" || payload == "{}" {
			continue
		}
		var p mishapPayload
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			// Best-effort, the injectedItemIDs precedent: one unreadable payload
			// drops that event's linkage, never the whole query.
			continue
		}
		if len(p.ItemIDs) == 0 {
			continue
		}
		ts, err := core.ParseTime(tsStr)
		if err != nil {
			return nil, fmt.Errorf("store.RecentMishapItemIDs: parse ts: %w", err)
		}
		// Rows arrive oldest-first, so the last write per id is its most recent.
		for _, id := range p.ItemIDs {
			if id != "" {
				out[id] = ts
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.RecentMishapItemIDs: rows: %w", err)
	}
	return out, nil
}
