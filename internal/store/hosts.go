package store

// Cross-machine observability: which machines have been talking to this daemon,
// and what it declined to do on their behalf.
//
// Both queries exist for one caller -- `seamlessd doctor` -- and both answer a
// question that only became askable once several devices could share one daemon.
// A shared daemon degrades silently by design (a remote agent simply gets no
// plan capture, no git stamp, no transcript harvest), so the operator needs a
// surface that says so out loud; these are it.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// HostActivity is one machine's session count over a window.
type HostActivity struct {
	// Host is the machine name a client reported. "" is the unnamed bucket:
	// sessions recorded before host scoping, or by a client too old to send one.
	// It is NOT a separate machine, and doctor must not count it as remote.
	Host     string
	Sessions int
}

// SessionHostsSince counts sessions per host updated since the cutoff, most
// active first. It is the raw material for doctor's "remote sessions" line: the
// local host's own row is in there too, because the useful statement is "3 of 11
// sessions came from elsewhere", not a bare remote count.
func SessionHostsSince(ctx context.Context, db *sql.DB, since time.Time) ([]HostActivity, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT host, COUNT(*) FROM sessions WHERE updated_at >= ?
		 GROUP BY host ORDER BY COUNT(*) DESC, host`,
		core.FormatTime(since))
	if err != nil {
		return nil, fmt.Errorf("store.SessionHostsSince: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []HostActivity
	for rows.Next() {
		var a HostActivity
		if err := rows.Scan(&a.Host, &a.Sessions); err != nil {
			return nil, fmt.Errorf("store.SessionHostsSince: scan: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.SessionHostsSince: %w", err)
	}
	return out, nil
}

// RemoteSkipsSince counts the daemon-side captures skipped because the agent was
// on another machine (the hook.error events stamped `remote-host-skip`) since
// the cutoff.
//
// This is the number that makes the design's deliberate silence visible: plan
// capture, git stamps and transcript harvest all read the DAEMON's disk, so they
// are skipped for a remote agent rather than answered from the wrong filesystem.
// Without this line the operator sees a remote device working and no captures
// appearing, with nothing anywhere saying why.
func RemoteSkipsSince(ctx context.Context, db *sql.DB, since time.Time) (int, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events
		 WHERE kind = ? AND ts >= ? AND json_extract(payload, '$.stage') = 'remote-host-skip'`,
		string(core.EventHookError), core.FormatTime(since)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store.RemoteSkipsSince: %w", err)
	}
	return n, nil
}
