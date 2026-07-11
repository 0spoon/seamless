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
	"sync"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// subBuffer is the per-subscriber channel depth. A subscriber that falls behind
// simply drops events (the console's live feed is best-effort; the DB remains the
// source of truth), so a stalled SSE client never blocks the write path.
const subBuffer = 32

// Recorder appends events to the log, reads them back, and fans successfully
// recorded events out to live subscribers (the console SSE feed).
type Recorder struct {
	db *sql.DB

	mu     sync.Mutex
	subs   map[int]chan core.Event
	nextID int
}

// NewRecorder returns a Recorder backed by db.
func NewRecorder(db *sql.DB) *Recorder { return &Recorder{db: db} }

// Subscribe registers a live-event channel and returns it with an unsubscribe
// func the caller must invoke when done (idempotent). Events are delivered
// best-effort: a full channel drops rather than blocks.
func (r *Recorder) Subscribe() (<-chan core.Event, func()) {
	ch := make(chan core.Event, subBuffer)
	r.mu.Lock()
	if r.subs == nil {
		r.subs = make(map[int]chan core.Event)
	}
	id := r.nextID
	r.nextID++
	r.subs[id] = ch
	r.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			r.mu.Lock()
			if c, ok := r.subs[id]; ok {
				delete(r.subs, id)
				close(c)
			}
			r.mu.Unlock()
		})
	}
}

// publish fans an event out to current subscribers, dropping to any that are
// full. It holds the lock so it cannot race an unsubscribe's close.
func (r *Recorder) publish(e core.Event) {
	r.mu.Lock()
	for _, ch := range r.subs {
		select {
		case ch <- e:
		default:
		}
	}
	r.mu.Unlock()
}

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
	r.publish(e)
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
	return scanEvents(rows)
}

// BySession returns a session's events in chronological order (oldest first), so
// a caller can render the session's timeline. A non-positive limit defaults to
// 500.
func (r *Recorder) BySession(ctx context.Context, sessionID string, limit int) ([]core.Event, error) {
	if sessionID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, ts, kind, session_id, project_slug, item_id, payload
		 FROM events WHERE session_id = ? ORDER BY ts ASC, id ASC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("events.BySession: %w", err)
	}
	return scanEvents(rows)
}

// scanEvents drains an events query into core.Event values.
func scanEvents(rows *sql.Rows) ([]core.Event, error) {
	defer func() { _ = rows.Close() }()
	var out []core.Event
	for rows.Next() {
		var (
			e                 core.Event
			ts, kind, payload string
		)
		if err := rows.Scan(&e.ID, &ts, &kind, &e.SessionID, &e.ProjectSlug, &e.ItemID, &payload); err != nil {
			return nil, fmt.Errorf("events: scan: %w", err)
		}
		t, err := core.ParseTime(ts)
		if err != nil {
			return nil, fmt.Errorf("events: parse ts: %w", err)
		}
		e.TS = t
		e.Kind = core.EventKind(kind)
		if payload != "" && payload != "{}" {
			if err := json.Unmarshal([]byte(payload), &e.Payload); err != nil {
				return nil, fmt.Errorf("events: unmarshal payload: %w", err)
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: rows: %w", err)
	}
	return out, nil
}
