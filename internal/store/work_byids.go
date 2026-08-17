// Batched by-id loaders for the work-record kinds. Recall hydrates a fused
// candidate set of mixed kinds, so each kind needs one query for the winners
// rather than one query per hit; these are the task/trial/session counterparts
// of MemoriesByIDs and NotesByIDs.
package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/0spoon/seamless/internal/core"
)

// TasksByIDs returns the tasks for the given ids keyed by id; missing ids are
// simply absent. Dependency edges are NOT populated: the callers are retrieval
// surfaces that show a task's identity and status, and loading edges for every
// hydrated hit would cost a second query for data nothing displays.
func TasksByIDs(ctx context.Context, db *sql.DB, ids []string) (map[string]core.Task, error) {
	out := make(map[string]core.Task, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, `SELECT `+taskCols+`
		FROM tasks WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("store.TasksByIDs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("store.TasksByIDs: scan: %w", err)
		}
		out[t.ID] = t
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.TasksByIDs: %w", err)
	}
	return out, nil
}

// TrialsByIDs returns the trials for the given ids keyed by id; missing ids are
// simply absent.
func TrialsByIDs(ctx context.Context, db *sql.DB, ids []string) (map[string]core.Trial, error) {
	out := make(map[string]core.Trial, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, `SELECT `+trialCols+`
		FROM trials WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("store.TrialsByIDs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		tr, err := scanTrial(rows)
		if err != nil {
			return nil, fmt.Errorf("store.TrialsByIDs: scan: %w", err)
		}
		out[tr.ID] = tr
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.TrialsByIDs: %w", err)
	}
	return out, nil
}

// SessionsByIDs returns the sessions for the given ids keyed by id; missing ids
// are simply absent.
func SessionsByIDs(ctx context.Context, db *sql.DB, ids []string) (map[string]core.Session, error) {
	out := make(map[string]core.Session, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, `SELECT `+sessionCols+`
		FROM sessions WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("store.SessionsByIDs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store.SessionsByIDs: scan: %w", err)
		}
		out[s.ID] = s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.SessionsByIDs: %w", err)
	}
	return out, nil
}
