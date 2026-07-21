// Favorite setters for the DB-owned entities. Memories and notes are not here:
// their favorite flag lives in file frontmatter (the source of truth) and
// reaches the index through the files layer's re-index, never a direct UPDATE.
// A star is metadata, not authorship, so none of these bump updated_at --
// starring must not churn recency sorts or the briefing recency trim.
package store

import (
	"context"
	"database/sql"
	"fmt"
)

// SetProjectFavorite sets or clears a project's favorite flag. Idempotent; an
// unknown slug affects no rows and returns nil (matching RetireProject).
func SetProjectFavorite(ctx context.Context, db *sql.DB, slug string, favorite bool) error {
	_, err := db.ExecContext(ctx,
		`UPDATE projects SET favorite = ? WHERE slug = ?`, boolToInt(favorite), slug)
	if err != nil {
		return fmt.Errorf("store.SetProjectFavorite: %w", err)
	}
	return nil
}

// SetTaskFavorite sets or clears a task's favorite flag. Idempotent; an unknown
// id affects no rows and returns nil.
func SetTaskFavorite(ctx context.Context, db *sql.DB, id string, favorite bool) error {
	_, err := db.ExecContext(ctx,
		`UPDATE tasks SET favorite = ? WHERE id = ?`, boolToInt(favorite), id)
	if err != nil {
		return fmt.Errorf("store.SetTaskFavorite: %w", err)
	}
	return nil
}

// SetSessionFavorite sets or clears a session's favorite flag. Idempotent; an
// unknown id affects no rows and returns nil.
func SetSessionFavorite(ctx context.Context, db *sql.DB, id string, favorite bool) error {
	_, err := db.ExecContext(ctx,
		`UPDATE sessions SET favorite = ? WHERE id = ?`, boolToInt(favorite), id)
	if err != nil {
		return fmt.Errorf("store.SetSessionFavorite: %w", err)
	}
	return nil
}

// SetTrialFavorite sets or clears a trial's favorite flag. Idempotent; an
// unknown id affects no rows and returns nil.
func SetTrialFavorite(ctx context.Context, db *sql.DB, id string, favorite bool) error {
	_, err := db.ExecContext(ctx,
		`UPDATE trials SET favorite = ? WHERE id = ?`, boolToInt(favorite), id)
	if err != nil {
		return fmt.Errorf("store.SetTrialFavorite: %w", err)
	}
	return nil
}
