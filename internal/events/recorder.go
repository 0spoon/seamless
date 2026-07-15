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
	"strings"
	"sync"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// truncationMarker is appended to a captured field that Truncate had to trim.
const truncationMarker = "\n… [truncated]"

// Truncate caps s to maxRunes runes, appending truncationMarker when it trims.
// A non-positive maxRunes disables truncation (returns s unchanged) -- the
// default for captured Interactions content, where retention pruning rather than
// truncation is the growth control. Rune-safe: never splits a multibyte rune.
func Truncate(s string, maxRunes int) string {
	if maxRunes <= 0 || len(s) <= maxRunes {
		// len (bytes) >= rune count, so a byte-length under the cap is safe.
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + truncationMarker
}

// kindArgs builds the "?,?,?" placeholder list and the matching []any args for
// an IN/NOT IN clause over kinds. It returns ("", nil) for an empty slice.
func kindArgs(kinds []core.EventKind) (string, []any) {
	if len(kinds) == 0 {
		return "", nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
	args := make([]any, len(kinds))
	for i, k := range kinds {
		args[i] = string(k)
	}
	return ph, args
}

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

// ByID returns a single event by its id. ok is false (with a nil error) when no
// event has that id.
func (r *Recorder) ByID(ctx context.Context, id string) (core.Event, bool, error) {
	if id == "" {
		return core.Event{}, false, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, ts, kind, session_id, project_slug, item_id, payload
		 FROM events WHERE id = ? LIMIT 1`, id)
	if err != nil {
		return core.Event{}, false, fmt.Errorf("events.ByID: %w", err)
	}
	evs, err := scanEvents(rows)
	if err != nil {
		return core.Event{}, false, err
	}
	if len(evs) == 0 {
		return core.Event{}, false, nil
	}
	return evs[0], true, nil
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

// ByKinds returns events of any of the given kinds, newest first, strictly older
// than the compound (beforeTS, beforeID) cursor when it is set -- so a caller can
// page backwards through the Interactions feed without missing or repeating rows
// across a ts tie (ids are ULIDs, lexically ordered like ts). An empty kinds
// slice returns nil; a non-positive limit defaults to 200.
func (r *Recorder) ByKinds(ctx context.Context, kinds []core.EventKind, beforeTS, beforeID string, limit int) ([]core.Event, error) {
	ph, args := kindArgs(kinds)
	if ph == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT id, ts, kind, session_id, project_slug, item_id, payload
	      FROM events WHERE kind IN (` + ph + `)`
	if beforeTS != "" {
		q += ` AND (ts < ? OR (ts = ? AND id < ?))`
		args = append(args, beforeTS, beforeTS, beforeID)
	}
	q += ` ORDER BY ts DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("events.ByKinds: %w", err)
	}
	return scanEvents(rows)
}

// ByKindsSince returns events of any of the given kinds strictly newer than the
// (sinceTS, sinceID) cursor, oldest first -- the gap-fill query an SSE client
// runs after a reconnect to recover rows it missed. An empty kinds slice returns
// nil; a non-positive limit defaults to 200.
func (r *Recorder) ByKindsSince(ctx context.Context, kinds []core.EventKind, sinceTS, sinceID string, limit int) ([]core.Event, error) {
	ph, args := kindArgs(kinds)
	if ph == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT id, ts, kind, session_id, project_slug, item_id, payload
	      FROM events WHERE kind IN (` + ph + `)`
	if sinceTS != "" {
		q += ` AND (ts > ? OR (ts = ? AND id > ?))`
		args = append(args, sinceTS, sinceTS, sinceID)
	}
	q += ` ORDER BY ts ASC, id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("events.ByKindsSince: %w", err)
	}
	return scanEvents(rows)
}

// KindTick is one event's timestamp and kind -- the minimal projection the
// console buckets into the interaction-volume histogram.
type KindTick struct {
	TS   time.Time
	Kind string
}

// KindTimeline returns the (ts, kind) of every event of the given kinds at or
// after sinceTS, newest first and capped at limit. project scopes to one project
// slug when non-empty. Only two columns are read, so a wide window stays cheap.
// An empty sinceTS spans all history (bounded by limit); an empty kinds slice or
// non-positive limit returns nil.
func (r *Recorder) KindTimeline(ctx context.Context, kinds []core.EventKind, project, sinceTS string, limit int) ([]KindTick, error) {
	ph, args := kindArgs(kinds)
	if ph == "" || limit <= 0 {
		return nil, nil
	}
	q := `SELECT ts, kind FROM events WHERE kind IN (` + ph + `)`
	if project != "" {
		q += ` AND project_slug = ?`
		args = append(args, project)
	}
	if sinceTS != "" {
		q += ` AND ts >= ?`
		args = append(args, sinceTS)
	}
	q += ` ORDER BY ts DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("events.KindTimeline: %w", err)
	}
	defer rows.Close()
	var out []KindTick
	for rows.Next() {
		var tsStr, kind string
		if err := rows.Scan(&tsStr, &kind); err != nil {
			return nil, fmt.Errorf("events.KindTimeline scan: %w", err)
		}
		ts, err := core.ParseTime(tsStr)
		if err != nil {
			return nil, fmt.Errorf("events.KindTimeline time: %w", err)
		}
		out = append(out, KindTick{TS: ts, Kind: kind})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events.KindTimeline: %w", err)
	}
	return out, nil
}

// RecentExcluding returns the most recent events, newest first, omitting the
// given kinds -- the overview's business-level feed, which hides transport-level
// tool.call / hook.prompt noise. A non-positive limit defaults to 50.
func (r *Recorder) RecentExcluding(ctx context.Context, limit int, exclude ...core.EventKind) ([]core.Event, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, ts, kind, session_id, project_slug, item_id, payload FROM events`
	var args []any
	if ph, exArgs := kindArgs(exclude); ph != "" {
		q += ` WHERE kind NOT IN (` + ph + `)`
		args = append(args, exArgs...)
	}
	q += ` ORDER BY ts DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("events.RecentExcluding: %w", err)
	}
	return scanEvents(rows)
}

// PruneKinds deletes events of the given kinds recorded strictly before cutoff,
// returning the number removed. It is the retention path for transport-level
// Interactions events (tool.call, hook.prompt); domain events are never passed
// in. An empty kinds slice or zero cutoff deletes nothing.
func (r *Recorder) PruneKinds(ctx context.Context, kinds []core.EventKind, before time.Time) (int64, error) {
	ph, args := kindArgs(kinds)
	if ph == "" || before.IsZero() {
		return 0, nil
	}
	args = append(args, core.FormatTime(before))
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM events WHERE kind IN (`+ph+`) AND ts < ?`, args...)
	if err != nil {
		return 0, fmt.Errorf("events.PruneKinds: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("events.PruneKinds: rows affected: %w", err)
	}
	return n, nil
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
