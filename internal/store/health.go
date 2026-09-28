package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// ClientSeen is one agent client (the session's external_client: claude-code,
// codex, ...) that has recorded sessions here, with its latest activity.
type ClientSeen struct {
	Client   string
	LastSeen time.Time
}

// LastBriefing is the most recent SessionStart briefing an agent received: the
// retrieval.injected event at the session-start surface.
type LastBriefing struct {
	EventID string
	At      time.Time
}

// HealthFacts are the cheap facts behind the console Home health strip -- the
// answer to "is it working?". Each is absent rather than zero when nothing
// backs it: no client has recorded a session yet, or no briefing was ever
// delivered.
type HealthFacts struct {
	// Clients are the agent clients seen, most recently active first.
	Clients []ClientSeen
	// LastBriefing is nil until a SessionStart briefing has been delivered.
	LastBriefing *LastBriefing
}

// GetHealthFacts reads the health strip's facts in two small queries: a
// group-by over the sessions' client, and the newest session-start injection
// walked down the (kind, ts) index.
//
// A session's client is its external_client, or -- for a row that predates
// that column or was named by hand -- the ambient name prefix (cc/, cx/), the
// same fallback the console's agent pill uses (and migration 010's backfill).
// A session with neither names no client and adds no row.
func GetHealthFacts(ctx context.Context, db *sql.DB) (HealthFacts, error) {
	var out HealthFacts
	rows, err := db.QueryContext(ctx, `
		SELECT client, MAX(updated_at) FROM (
			SELECT CASE
			         WHEN external_client <> '' THEN external_client
			         WHEN substr(name, 1, 3) = 'cc/' THEN 'claude-code'
			         WHEN substr(name, 1, 3) = 'cx/' THEN 'codex'
			         ELSE ''
			       END AS client, updated_at
			  FROM sessions)
		 WHERE client <> ''
		 GROUP BY client
		 ORDER BY MAX(updated_at) DESC, client`)
	if err != nil {
		return out, fmt.Errorf("store.GetHealthFacts: clients: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var client, last string
		if err := rows.Scan(&client, &last); err != nil {
			return out, fmt.Errorf("store.GetHealthFacts: scan client: %w", err)
		}
		at, err := core.ParseTime(last)
		if err != nil {
			return out, fmt.Errorf("store.GetHealthFacts: client %s: %w", client, err)
		}
		out.Clients = append(out.Clients, ClientSeen{Client: client, LastSeen: at})
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("store.GetHealthFacts: clients: %w", err)
	}

	// "session-start" is the briefing surface (InjectionSurfaces[0]); the other
	// injections -- prompt recall, subagent starts -- are not the briefing.
	var id, ts string
	err = db.QueryRowContext(ctx, `
		SELECT id, ts FROM events
		 WHERE kind = ? AND json_extract(payload, '$.hook') = 'session-start'
		 ORDER BY ts DESC LIMIT 1`, string(core.EventInjected)).Scan(&id, &ts)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return out, nil
	case err != nil:
		return out, fmt.Errorf("store.GetHealthFacts: last briefing: %w", err)
	}
	at, err := core.ParseTime(ts)
	if err != nil {
		return out, fmt.Errorf("store.GetHealthFacts: last briefing %s: %w", id, err)
	}
	out.LastBriefing = &LastBriefing{EventID: id, At: at}
	return out, nil
}
