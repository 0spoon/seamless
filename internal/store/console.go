package store

import (
	"context"
	"database/sql"
	"fmt"
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
