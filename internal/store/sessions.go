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

const sessionCols = `id, name, project_slug, status, findings, claude_session_id,
	cwd, source, ambient, metadata, created_at, updated_at`

// ErrSessionNameExists is returned by CreateSession when the name is already
// taken (sessions.name is UNIQUE). It mirrors ErrSlugExists so a caller racing
// to create a named session can tell "someone beat me to this name" (resume it)
// apart from a real database failure, instead of having to match on error text.
//
// It reads any uniqueness failure on the insert as a name collision. The table's
// other unique constraint is the id primary key, and callers mint a fresh ULID
// per call, so a PK collision cannot happen without an id-generation bug -- the
// same assumption ErrSlugExists makes.
var ErrSessionNameExists = errors.New("store: session name already exists")

// CreateSession inserts a session. The caller mints the ULID id; a duplicate
// name returns ErrSessionNameExists.
func CreateSession(ctx context.Context, db *sql.DB, s core.Session) error {
	meta, err := marshalMetadata(s.Metadata)
	if err != nil {
		return fmt.Errorf("store.CreateSession: %w", err)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO sessions
		    (id, name, project_slug, status, findings, claude_session_id,
		     cwd, source, ambient, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.ProjectSlug, string(s.Status), s.Findings, s.ClaudeSessionID,
		s.CWD, s.Source, boolToInt(s.Ambient), meta,
		core.FormatTime(s.CreatedAt), core.FormatTime(s.UpdatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("store.CreateSession: %w: %q", ErrSessionNameExists, s.Name)
		}
		return fmt.Errorf("store.CreateSession: %w", err)
	}
	return nil
}

// UpdateSession updates the mutable fields of a session (status, findings,
// metadata, project scope, updated_at) by id.
func UpdateSession(ctx context.Context, db *sql.DB, s core.Session) error {
	meta, err := marshalMetadata(s.Metadata)
	if err != nil {
		return fmt.Errorf("store.UpdateSession: %w", err)
	}
	res, err := db.ExecContext(ctx, `
		UPDATE sessions
		   SET project_slug = ?, status = ?, findings = ?, metadata = ?, updated_at = ?
		 WHERE id = ?`,
		s.ProjectSlug, string(s.Status), s.Findings, meta,
		core.FormatTime(s.UpdatedAt), s.ID)
	if err != nil {
		return fmt.Errorf("store.UpdateSession: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store.UpdateSession: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store.UpdateSession: no session with id %q", s.ID)
	}
	return nil
}

// ReactivateSessionByName resumes the named session with a single targeted
// UPDATE: status flips back to active, project_slug is re-scoped (only when
// project is non-empty), and updated_at is bumped -- findings and metadata are
// never touched. The SessionStart hook resumes ambient sessions through it
// instead of a full-row read-modify-write, so a concurrent transcript harvest
// (which writes findings via UpdateSession) cannot be clobbered by a racing
// resume that read the row before the harvest landed. found is false when no
// session has that name.
func ReactivateSessionByName(ctx context.Context, db *sql.DB, name, project string, now time.Time) (bool, error) {
	if name == "" {
		return false, nil
	}
	res, err := db.ExecContext(ctx, `
		UPDATE sessions
		   SET status = 'active',
		       project_slug = CASE WHEN ? = '' THEN project_slug ELSE ? END,
		       updated_at = ?
		 WHERE name = ?`,
		project, project, core.FormatTime(now.UTC()), name)
	if err != nil {
		return false, fmt.Errorf("store.ReactivateSessionByName: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store.ReactivateSessionByName: rows affected: %w", err)
	}
	return n > 0, nil
}

// ActiveSessionIDs reports which of the given session ids belong to a currently
// active session. It backs the MCP server's connection-binding sweep: a binding
// whose session is no longer active -- ended by the session_end tool, the
// SessionEnd hook, or the idle reaper -- is evicted. Ids absent from the result
// (unknown or non-active) are simply not set.
func ActiveSessionIDs(ctx context.Context, db *sql.DB, ids []string) (map[string]bool, error) {
	active := make(map[string]bool, len(ids))
	const chunkSize = 500 // stay well under SQLite's bound-parameter limit
	for start := 0; start < len(ids); start += chunkSize {
		chunk := ids[start:min(start+chunkSize, len(ids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := db.QueryContext(ctx, `SELECT id FROM sessions
			WHERE status = 'active' AND id IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("store.ActiveSessionIDs: %w", err)
		}
		if err := scanIDs(rows, active); err != nil {
			return nil, fmt.Errorf("store.ActiveSessionIDs: %w", err)
		}
	}
	return active, nil
}

// scanIDs collects single-column id rows into dst, closing rows.
func scanIDs(rows *sql.Rows, dst map[string]bool) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		dst[id] = true
	}
	return rows.Err()
}

// TouchSession heartbeats an active session by bumping its updated_at, marking it
// still-alive for the idle reaper. It only touches active sessions (the WHERE
// guard), so it never resurrects a completed or expired one, and is a no-op on an
// empty id. Callers fire it on activity (a bound MCP tool call), best-effort.
func TouchSession(ctx context.Context, db *sql.DB, id string, now time.Time) error {
	if id == "" {
		return nil
	}
	_, err := db.ExecContext(ctx,
		`UPDATE sessions SET updated_at = ? WHERE id = ? AND status = 'active'`,
		core.FormatTime(now.UTC()), id)
	if err != nil {
		return fmt.Errorf("store.TouchSession: %w", err)
	}
	return nil
}

// TouchSessionByName is TouchSession keyed by the unique session name, for the
// ambient hooks which know the cc/{prefix} name (not the ULID). Same active-only
// guard and no-op-on-empty semantics.
func TouchSessionByName(ctx context.Context, db *sql.DB, name string, now time.Time) error {
	if name == "" {
		return nil
	}
	_, err := db.ExecContext(ctx,
		`UPDATE sessions SET updated_at = ? WHERE name = ? AND status = 'active'`,
		core.FormatTime(now.UTC()), name)
	if err != nil {
		return fmt.Errorf("store.TouchSessionByName: %w", err)
	}
	return nil
}

// ExpireStaleSessions reaps active sessions whose last activity predates cutoff,
// flipping each to SessionExpired. It leaves updated_at untouched so the row still
// records when the session was last alive (the console orders and dates by it),
// which also keeps the flip idempotent: a reaped session no longer matches the
// active filter. It returns only the sessions it actually flipped (id + project)
// so the caller can release their task claims and record telemetry -- a session
// that was heartbeated or gracefully completed between the candidate read and the
// flip is skipped, never falsely reaped. Passing updated_at unchanged means a
// later graceful session_end/resume can still upgrade the row (its guards key off
// completed/active, not expired).
func ExpireStaleSessions(ctx context.Context, db *sql.DB, cutoff time.Time) ([]core.Session, error) {
	candidates, err := staleActiveSessions(ctx, db, cutoff)
	if err != nil {
		return nil, err
	}
	var reaped []core.Session
	for _, s := range candidates {
		flipped, err := expireSessionIfStillStale(ctx, db, s.ID, cutoff)
		if err != nil {
			return nil, err
		}
		if flipped {
			reaped = append(reaped, s)
		}
	}
	return reaped, nil
}

// expireSessionIfStillStale flips one session to expired iff it is STILL an
// active session idle past cutoff at flip time, reporting whether it did. The
// re-check closes the window between the candidate SELECT and this UPDATE: a
// heartbeat (TouchSession bumping updated_at) or a graceful completion (status
// leaving 'active') that lands in between wins, and the session is not reaped.
func expireSessionIfStillStale(ctx context.Context, db *sql.DB, id string, cutoff time.Time) (bool, error) {
	res, err := db.ExecContext(ctx,
		`UPDATE sessions SET status = 'expired'
		  WHERE id = ? AND status = 'active' AND updated_at < ?`,
		id, core.FormatTime(cutoff.UTC()))
	if err != nil {
		return false, fmt.Errorf("store.ExpireStaleSessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store.ExpireStaleSessions: rows affected: %w", err)
	}
	return n == 1, nil
}

// staleActiveSessions returns the active sessions whose last activity predates
// cutoff, oldest first -- the reap candidate set. Split out so the read (with its
// deferred Close) is done before ExpireStaleSessions issues its UPDATEs.
func staleActiveSessions(ctx context.Context, db *sql.DB, cutoff time.Time) ([]core.Session, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+sessionCols+`
		FROM sessions
		WHERE status = 'active' AND updated_at < ?
		ORDER BY updated_at ASC`, core.FormatTime(cutoff.UTC()))
	if err != nil {
		return nil, fmt.Errorf("store.staleActiveSessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var stale []core.Session
	for rows.Next() {
		s, serr := scanSession(rows)
		if serr != nil {
			return nil, fmt.Errorf("store.staleActiveSessions: %w", serr)
		}
		stale = append(stale, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.staleActiveSessions: %w", err)
	}
	return stale, nil
}

// ActiveAmbientByCWD returns the active ambient (cc/*) sessions whose cwd matches,
// most recent first. session_start consults it to link a freshly created explicit
// session to the Claude session that owns the cwd (via its claude_session_id), so a
// graceful SessionEnd can close both at once instead of leaving the explicit one to
// the idle reaper. An empty cwd matches nothing (no basis to link).
func ActiveAmbientByCWD(ctx context.Context, db *sql.DB, cwd string) ([]core.Session, error) {
	if cwd == "" {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT `+sessionCols+`
		FROM sessions
		WHERE status = 'active' AND ambient = 1 AND cwd = ?
		ORDER BY updated_at DESC, id DESC`, cwd)
	if err != nil {
		return nil, fmt.Errorf("store.ActiveAmbientByCWD: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []core.Session
	for rows.Next() {
		s, serr := scanSession(rows)
		if serr != nil {
			return nil, fmt.Errorf("store.ActiveAmbientByCWD: %w", serr)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ActiveSessionsByClaudeID returns every active session -- the ambient cc/* plus
// any explicit session_start that linked to it -- stamped with claudeSessionID,
// ambient first. The SessionEnd hook uses it to complete a whole Claude session's
// sessions the moment we know it ended, rather than waiting out the idle TTL. An
// empty id matches nothing.
func ActiveSessionsByClaudeID(ctx context.Context, db *sql.DB, claudeSessionID string) ([]core.Session, error) {
	if claudeSessionID == "" {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT `+sessionCols+`
		FROM sessions
		WHERE status = 'active' AND claude_session_id = ?
		ORDER BY ambient DESC, updated_at DESC, id DESC`, claudeSessionID)
	if err != nil {
		return nil, fmt.Errorf("store.ActiveSessionsByClaudeID: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []core.Session
	for rows.Next() {
		s, serr := scanSession(rows)
		if serr != nil {
			return nil, fmt.Errorf("store.ActiveSessionsByClaudeID: %w", serr)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SessionByID returns the session with the given id. found is false when absent.
func SessionByID(ctx context.Context, db *sql.DB, id string) (core.Session, bool, error) {
	return sessionOne(ctx, db, `SELECT `+sessionCols+` FROM sessions WHERE id = ? LIMIT 1`, id)
}

// SessionByName returns the session with the given (unique) name. found is false
// when absent.
func SessionByName(ctx context.Context, db *sql.DB, name string) (core.Session, bool, error) {
	return sessionOne(ctx, db, `SELECT `+sessionCols+` FROM sessions WHERE name = ? LIMIT 1`, name)
}

// RecentFindings returns completed sessions with meaningful findings visible to a
// project (its own plus global sessions), most recent first. It backs the
// briefing's "recent findings" section, so it excludes both blank findings and
// the core.FindingNoSummary sentinel (a session that ended with nothing to
// harvest) -- a content-free line is not worth an agent's context.
func RecentFindings(ctx context.Context, db *sql.DB, project string, limit int) ([]core.Session, error) {
	if limit <= 0 {
		limit = 3
	}
	rows, err := db.QueryContext(ctx, `SELECT `+sessionCols+`
		FROM sessions
		WHERE status = 'completed' AND findings <> '' AND findings <> ?
		  AND (project_slug = ? OR project_slug = '')
		ORDER BY updated_at DESC, id DESC LIMIT ?`, core.FindingNoSummary, project, limit)
	if err != nil {
		return nil, fmt.Errorf("store.RecentFindings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []core.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store.RecentFindings: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListSessions returns sessions newest-updated first, optionally filtered by
// status and to those updated since a cutoff (a zero `since` means all time),
// capped at limit (default 100). It backs the console Sessions list.
func ListSessions(ctx context.Context, db *sql.DB, status core.SessionStatus, since time.Time, limit int) ([]core.Session, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT ` + sessionCols + ` FROM sessions`
	args := []any{}
	where := ""
	add := func(cond string, val any) {
		if where == "" {
			where = " WHERE " + cond
		} else {
			where += " AND " + cond
		}
		args = append(args, val)
	}
	if status != "" {
		add(`status = ?`, string(status))
	}
	if !since.IsZero() {
		add(`updated_at >= ?`, core.FormatTime(since))
	}
	query += where + ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store.ListSessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []core.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store.ListSessions: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// LatestActiveAmbientSessionForProject returns the most recently updated active
// ambient (cc/*) session in the given project, updated within the window, or
// found=false when none. It backs the MCP write-scope fallback: an agent that
// writes without calling session_start inherits its project's ambient session's
// provenance. A non-positive within disables the recency filter. Scoping to a
// single project is what prevents cross-agent bleed -- see ActiveAmbientProjects.
func LatestActiveAmbientSessionForProject(ctx context.Context, db *sql.DB, project string, within time.Duration) (core.Session, bool, error) {
	query := `SELECT ` + sessionCols + ` FROM sessions
		WHERE status = 'active' AND ambient = 1 AND project_slug = ?`
	args := []any{project}
	if within > 0 {
		query += ` AND updated_at >= ?`
		args = append(args, core.FormatTime(time.Now().UTC().Add(-within)))
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT 1`
	return sessionOne(ctx, db, query, args...)
}

// ActiveAmbientProjects returns the distinct project slugs that have at least one
// active ambient (cc/*) session updated within the window, ordered by each
// project's most recent activity. The MCP fallback consults it to tell a safe
// single-project inference (len 1) from the ambiguous concurrent-agent case
// (len > 1, agents in different repos) where guessing would bleed a write into
// the wrong project. A non-positive within disables the recency filter. The
// global scope is reported as the empty string, distinct from any named project.
func ActiveAmbientProjects(ctx context.Context, db *sql.DB, within time.Duration) ([]string, error) {
	query := `SELECT project_slug FROM sessions
		WHERE status = 'active' AND ambient = 1`
	args := []any{}
	if within > 0 {
		query += ` AND updated_at >= ?`
		args = append(args, core.FormatTime(time.Now().UTC().Add(-within)))
	}
	query += ` GROUP BY project_slug ORDER BY MAX(updated_at) DESC`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store.ActiveAmbientProjects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, fmt.Errorf("store.ActiveAmbientProjects: %w", err)
		}
		out = append(out, slug)
	}
	return out, rows.Err()
}

// ActiveAmbientSessionsForProject returns every active ambient (cc/*) session in
// the given project updated within the window, most recent first. resolveSession
// uses it to refuse targeting a session by inference when more than one same-
// project ambient could be the one meant -- two agents in the same repo -- so a
// session_update/end without an explicit id can't complete a sibling's session.
// A non-positive within disables the recency filter.
func ActiveAmbientSessionsForProject(ctx context.Context, db *sql.DB, project string, within time.Duration) ([]core.Session, error) {
	query := `SELECT ` + sessionCols + ` FROM sessions
		WHERE status = 'active' AND ambient = 1 AND project_slug = ?`
	args := []any{project}
	if within > 0 {
		query += ` AND updated_at >= ?`
		args = append(args, core.FormatTime(time.Now().UTC().Add(-within)))
	}
	query += ` ORDER BY updated_at DESC, id DESC`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store.ActiveAmbientSessionsForProject: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []core.Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store.ActiveAmbientSessionsForProject: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// SiblingFindings returns the most recent completed sessions with meaningful
// findings across the given sibling projects, newest first, capped at limit. It
// backs the briefing's "Sibling projects" section, so -- like RecentFindings --
// it excludes blank findings and the core.FindingNoSummary sentinel. An empty
// slugs slice yields no results.
func SiblingFindings(ctx context.Context, db *sql.DB, slugs []string, limit int) ([]core.Session, error) {
	if len(slugs) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 2
	}
	args := make([]any, 0, len(slugs)+2)
	args = append(args, core.FindingNoSummary)
	for _, s := range slugs {
		args = append(args, s)
	}
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, `SELECT `+sessionCols+`
		FROM sessions
		WHERE status = 'completed' AND findings <> '' AND findings <> ?
		  AND project_slug IN (`+placeholders(len(slugs))+`)
		ORDER BY updated_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("store.SiblingFindings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []core.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store.SiblingFindings: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func sessionOne(ctx context.Context, db *sql.DB, query string, args ...any) (core.Session, bool, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return core.Session{}, false, fmt.Errorf("store: session query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return core.Session{}, false, rows.Err()
	}
	s, err := scanSession(rows)
	if err != nil {
		return core.Session{}, false, fmt.Errorf("store: scan session: %w", err)
	}
	return s, true, nil
}

func scanSession(rows *sql.Rows) (core.Session, error) {
	var (
		s                core.Session
		status, meta     string
		ambient          int
		created, updated string
	)
	if err := rows.Scan(
		&s.ID, &s.Name, &s.ProjectSlug, &status, &s.Findings, &s.ClaudeSessionID,
		&s.CWD, &s.Source, &ambient, &meta, &created, &updated,
	); err != nil {
		return core.Session{}, err
	}
	s.Status = core.SessionStatus(status)
	s.Ambient = ambient != 0
	if meta != "" && meta != "{}" {
		if err := json.Unmarshal([]byte(meta), &s.Metadata); err != nil {
			return core.Session{}, fmt.Errorf("metadata: %w", err)
		}
	}
	var err error
	if s.CreatedAt, err = core.ParseTime(created); err != nil {
		return core.Session{}, fmt.Errorf("created_at: %w", err)
	}
	if s.UpdatedAt, err = core.ParseTime(updated); err != nil {
		return core.Session{}, fmt.Errorf("updated_at: %w", err)
	}
	return s, nil
}

// marshalMetadata serializes a metadata map to a JSON object string ("{}" when
// empty), matching the sessions.metadata column default.
func marshalMetadata(m map[string]any) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal metadata: %w", err)
	}
	return string(b), nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
