// Package events is the append-only event log: the single write path for the
// record of everything (session lifecycle, memory write/read/supersede,
// injection, task transition, gardener action). Telemetry, retrieval stats, and
// the console feed all derive from this log. SSE fan-out is added in a later phase.
package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// Recorder appends events to the log and reads them back.
type Recorder struct {
	db *sql.DB
}

// NewRecorder returns a Recorder backed by db.
func NewRecorder(db *sql.DB) *Recorder { return &Recorder{db: db} }

// Record appends an event, stamping a ULID id and UTC timestamp when absent and
// serializing Payload to JSON. It returns the stored event's id. A non-fatal
// logging call site may ignore the id but should not ignore the error.
func (r *Recorder) Record(ctx context.Context, e core.Event) (string, error) {
	if e.Kind == "" {
		return "", fmt.Errorf("events.Record: empty kind")
	}
	if e.ID == "" {
		id, err := core.NewID()
		if err != nil {
			return "", fmt.Errorf("events.Record: %w", err)
		}
		e.ID = id
	}
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}

	payload := "{}"
	if len(e.Payload) > 0 {
		b, err := json.Marshal(e.Payload)
		if err != nil {
			return "", fmt.Errorf("events.Record: marshal payload: %w", err)
		}
		payload = string(b)
	}

	_, err := r.db.ExecContext(ctx,
		`INSERT INTO events (id, ts, kind, session_id, project_slug, item_id, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.ID, core.FormatTime(e.TS), string(e.Kind),
		e.SessionID, e.ProjectSlug, e.ItemID, payload,
	)
	if err != nil {
		return "", fmt.Errorf("events.Record: insert: %w", err)
	}
	return e.ID, nil
}

// Recent returns the most recent events, newest first. A non-positive limit
// defaults to 50.
func (r *Recorder) Recent(ctx context.Context, limit int) ([]core.Event, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, ts, kind, session_id, project_slug, item_id, payload
		 FROM events ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("events.Recent: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []core.Event
	for rows.Next() {
		var (
			e                 core.Event
			ts, kind, payload string
		)
		if err := rows.Scan(&e.ID, &ts, &kind, &e.SessionID, &e.ProjectSlug, &e.ItemID, &payload); err != nil {
			return nil, fmt.Errorf("events.Recent: scan: %w", err)
		}
		t, err := core.ParseTime(ts)
		if err != nil {
			return nil, fmt.Errorf("events.Recent: parse ts: %w", err)
		}
		e.TS = t
		e.Kind = core.EventKind(kind)
		if payload != "" && payload != "{}" {
			if err := json.Unmarshal([]byte(payload), &e.Payload); err != nil {
				return nil, fmt.Errorf("events.Recent: unmarshal payload: %w", err)
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events.Recent: rows: %w", err)
	}
	return out, nil
}
