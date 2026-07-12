package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

const sessionCols = `id, name, project_slug, status, findings, claude_session_id,
	cwd, source, ambient, metadata, created_at, updated_at`

// CreateSession inserts a session. The caller mints the ULID id and ensures the
// name is unique (sessions.name is UNIQUE); a duplicate name is an error.
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
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("store.UpdateSession: no session with id %q", s.ID)
	}
	return nil
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

// RecentFindings returns completed sessions with non-empty findings visible to a
// project (its own plus global sessions), most recent first. It backs the
// briefing's "recent findings" section.
func RecentFindings(ctx context.Context, db *sql.DB, project string, limit int) ([]core.Session, error) {
	if limit <= 0 {
		limit = 3
	}
	rows, err := db.QueryContext(ctx, `SELECT `+sessionCols+`
		FROM sessions
		WHERE status = 'completed' AND findings <> ''
		  AND (project_slug = ? OR project_slug = '')
		ORDER BY updated_at DESC, id DESC LIMIT ?`, project, limit)
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
// status, capped at limit (default 100). It backs the console Sessions list.
func ListSessions(ctx context.Context, db *sql.DB, status core.SessionStatus, limit int) ([]core.Session, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT ` + sessionCols + ` FROM sessions`
	args := []any{}
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, string(status))
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
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

// SiblingFindings returns the most recent completed sessions with non-empty
// findings across the given sibling projects, newest first, capped at limit. It
// backs the briefing's "Sibling projects" section. An empty slugs slice yields
// no results.
func SiblingFindings(ctx context.Context, db *sql.DB, slugs []string, limit int) ([]core.Session, error) {
	if len(slugs) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 2
	}
	args := make([]any, 0, len(slugs)+1)
	for _, s := range slugs {
		args = append(args, s)
	}
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, `SELECT `+sessionCols+`
		FROM sessions
		WHERE status = 'completed' AND findings <> ''
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
