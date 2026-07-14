package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // sqlite driver for reading the v1 snapshot

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/store"
)

// Options configures an import run.
type Options struct {
	// SourceDir is the v1 data directory (e.g. ~/.seam). Its notes/ tree and
	// seam.db are read; nothing under it is modified.
	SourceDir string
	// SkipProjects are storage-tree segments to ignore (e.g. "briefings").
	SkipProjects []string
}

// Report summarizes what an import produced. Counts are of items actually
// written; Skipped counts items already present in v2 (idempotent re-import).
type Report struct {
	Memories int
	Notes    int
	Trials   int
	Sessions int
	Events   int
	Projects int // projects-table rows backfilled from imported items
	Skipped  int
	Warnings []string
}

// String renders a human-readable summary.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "imported: %d memories, %d notes, %d trials, %d sessions, %d events, %d projects (%d skipped, already present)",
		r.Memories, r.Notes, r.Trials, r.Sessions, r.Events, r.Projects, r.Skipped)
	if len(r.Warnings) > 0 {
		fmt.Fprintf(&b, "\nwarnings (%d):", len(r.Warnings))
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "\n  - %s", w)
		}
	}
	return b.String()
}

// Import migrates v1 data at opts.SourceDir into v2, writing memory/note files
// through mgr (so they are indexed and, if mgr has an embedder, embedded) and
// inserting trials/sessions/events directly into db. It is idempotent by id: an
// item already present in v2 is skipped.
func Import(ctx context.Context, mgr *files.Manager, db *sql.DB, opts Options) (*Report, error) {
	rep := &Report{}
	skip := make(map[string]bool, len(opts.SkipProjects))
	for _, p := range opts.SkipProjects {
		skip[p] = true
	}

	if err := importNotesTree(ctx, mgr, db, opts, skip, rep); err != nil {
		return rep, err
	}
	if err := importDB(ctx, db, opts, rep); err != nil {
		return rep, err
	}
	if err := backfillProjects(ctx, db, rep); err != nil {
		return rep, err
	}
	return rep, nil
}

// backfillProjects ensures a projects-table row exists for every project slug
// referenced by imported memories, notes, or trials, so project_list reflects
// the imported corpus. It runs on every import (including idempotent re-imports),
// registering only the slugs still missing a row. v1 sessions and events carry
// no project slug and are not a source here.
func backfillProjects(ctx context.Context, db *sql.DB, rep *Report) error {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT project FROM memories_index WHERE project <> ''
		UNION
		SELECT DISTINCT project FROM notes_index WHERE project <> ''
		UNION
		SELECT DISTINCT project_slug FROM trials WHERE project_slug <> ''`)
	if err != nil {
		return fmt.Errorf("importer: distinct projects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var slugs []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return fmt.Errorf("importer: scan project slug: %w", err)
		}
		slugs = append(slugs, slug)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("importer: distinct projects: %w", err)
	}

	for _, slug := range slugs {
		// Only canonical slugs become projects. Malformed values -- notably v1
		// inbox notes whose project field is actually a "<file>.md" name (the
		// inbox-note importer bug) -- slugify to something else and are skipped,
		// so the bug never pollutes project_list.
		if core.Slugify(slug) != slug {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("skipped non-slug project %q (not registered)", slug))
			continue
		}
		_, ok, err := store.ProjectBySlug(ctx, db, slug)
		if err != nil {
			return fmt.Errorf("importer: lookup project %q: %w", slug, err)
		}
		if ok {
			continue
		}
		if _, err := store.EnsureProject(ctx, db, slug, slug); err != nil {
			return fmt.Errorf("importer: ensure project %q: %w", slug, err)
		}
		rep.Projects++
	}
	return nil
}

func importNotesTree(ctx context.Context, mgr *files.Manager, db *sql.DB, opts Options, skip map[string]bool, rep *Report) error {
	notesRoot := filepath.Join(opts.SourceDir, "notes")
	if _, err := os.Stat(notesRoot); err != nil {
		return fmt.Errorf("importer: notes tree: %w", err)
	}

	return filepath.WalkDir(notesRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(notesRoot, path)
		if err != nil {
			return err
		}
		segs := strings.Split(filepath.ToSlash(rel), "/")
		// topSeg is the top-level storage segment: a project subdir, or -- for a
		// file directly under notes/ -- the file itself. It drives skip decisions.
		topSeg := segs[0]

		if d.IsDir() {
			if skip[topSeg] {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" || skip[topSeg] {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		// A note directly under notes/ has no leading directory segment and
		// belongs to no project -- the inbox. Only a leading directory names a
		// project; treating the filename as the project was the inbox-note bug.
		project := ""
		if len(segs) > 1 {
			project = segs[0]
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("importer: read %s: %w", rel, err)
		}
		fm, body, err := parseV1(string(content))
		if err != nil {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: parse: %v", rel, err))
			return nil
		}
		if fm.ID == "" {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: no id, skipped", rel))
			return nil
		}

		switch classify(fm) {
		case classMemory:
			return importMemory(ctx, mgr, db, fm, body, rel, rep)
		case classTrial:
			return importTrial(ctx, db, fm, body, project, rep)
		default:
			slug := strings.TrimSuffix(filepath.Base(path), ".md")
			return importNote(ctx, mgr, db, fm, body, project, slug, rep)
		}
	})
}

func importMemory(ctx context.Context, mgr *files.Manager, db *sql.DB, fm v1Frontmatter, body, rel string, rep *Report) error {
	exists, err := idExists(ctx, db, "memories_index", fm.ID)
	if err != nil {
		return err
	}
	if exists {
		rep.Skipped++
		return nil
	}
	mem, warn := memoryFromV1(fm, body)
	if warn != "" {
		rep.Warnings = append(rep.Warnings, rel+": "+warn)
	}
	if _, err := mgr.WriteMemory(ctx, mem); err != nil {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: write memory: %v", rel, err))
		return nil
	}
	rep.Memories++
	return nil
}

func importNote(ctx context.Context, mgr *files.Manager, db *sql.DB, fm v1Frontmatter, body, project, slug string, rep *Report) error {
	exists, err := idExists(ctx, db, "notes_index", fm.ID)
	if err != nil {
		return err
	}
	if exists {
		rep.Skipped++
		return nil
	}
	if _, err := mgr.WriteNote(ctx, noteFromV1(fm, body, project, slug)); err != nil {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: write note: %v", slug, err))
		return nil
	}
	rep.Notes++
	return nil
}

func importTrial(ctx context.Context, db *sql.DB, fm v1Frontmatter, body, project string, rep *Report) error {
	tr := trialFromV1(fm, body, project)
	metrics, err := json.Marshal(tr.Metrics)
	if err != nil {
		return fmt.Errorf("importer: marshal metrics: %w", err)
	}
	res, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO trials
		    (id, lab, title, changes, expected, actual, outcome, metrics, session_id, project_slug, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?)`,
		tr.ID, tr.Lab, tr.Title, tr.Changes, tr.Expected, tr.Actual, string(tr.Outcome),
		string(metrics), tr.ProjectSlug, core.FormatTime(tr.CreatedAt))
	if err != nil {
		return fmt.Errorf("importer: insert trial %s: %w", tr.ID, err)
	}
	countInsert(res, &rep.Trials, &rep.Skipped)
	return nil
}

// importDB replays agent_sessions -> sessions and agent_tool_calls -> events.
func importDB(ctx context.Context, db *sql.DB, opts Options, rep *Report) error {
	srcPath := filepath.Join(opts.SourceDir, "seam.db")
	if _, err := os.Stat(srcPath); err != nil {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("no seam.db at %s, skipping sessions/events", srcPath))
		return nil
	}
	src, err := sql.Open("sqlite", "file:"+url.PathEscape(srcPath)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("importer: open v1 db: %w", err)
	}
	defer func() { _ = src.Close() }()

	if err := importSessions(ctx, src, db, rep); err != nil {
		return err
	}
	return importEvents(ctx, src, db, rep)
}

func importSessions(ctx context.Context, src, db *sql.DB, rep *Report) error {
	rows, err := src.QueryContext(ctx,
		`SELECT id, name, COALESCE(findings,''), COALESCE(metadata,'{}'),
		        created_at, updated_at FROM agent_sessions`)
	if err != nil {
		return fmt.Errorf("importer: query agent_sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id, name, findings, metadata, created, updated string
		if err := rows.Scan(&id, &name, &findings, &metadata, &created, &updated); err != nil {
			return fmt.Errorf("importer: scan session: %w", err)
		}
		// Imported sessions are historical snapshots; none are live in v2.
		res, err := db.ExecContext(ctx, `
			INSERT OR IGNORE INTO sessions
			    (id, name, project_slug, status, findings, claude_session_id, cwd, source, ambient, metadata, created_at, updated_at)
			VALUES (?, ?, '', ?, ?, '', '', 'import', 0, ?, ?, ?)`,
			id, name, string(core.SessionCompleted), findings, metadata, reformatTime(created), reformatTime(updated))
		if err != nil {
			return fmt.Errorf("importer: insert session %s: %w", id, err)
		}
		countInsert(res, &rep.Sessions, &rep.Skipped)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("importer: query agent_sessions: %w", err)
	}
	return nil
}

func importEvents(ctx context.Context, src, db *sql.DB, rep *Report) error {
	rows, err := src.QueryContext(ctx,
		`SELECT id, COALESCE(session_id,''), tool_name, COALESCE(error,''),
		        COALESCE(duration_ms,0), created_at FROM agent_tool_calls`)
	if err != nil {
		return fmt.Errorf("importer: query agent_tool_calls: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id, sessionID, tool, errStr, created string
		var durationMS int64
		if err := rows.Scan(&id, &sessionID, &tool, &errStr, &durationMS, &created); err != nil {
			return fmt.Errorf("importer: scan tool call: %w", err)
		}
		payload := map[string]any{"tool": tool, "duration_ms": durationMS}
		if errStr != "" {
			payload["error"] = errStr
		}
		pj, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("importer: marshal event payload: %w", err)
		}
		res, err := db.ExecContext(ctx, `
			INSERT OR IGNORE INTO events (id, ts, kind, session_id, project_slug, item_id, payload)
			VALUES (?, ?, ?, ?, '', '', ?)`,
			id, reformatTime(created), string(core.EventToolCall), sessionID, string(pj))
		if err != nil {
			return fmt.Errorf("importer: insert event %s: %w", id, err)
		}
		countInsert(res, &rep.Events, &rep.Skipped)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("importer: query agent_tool_calls: %w", err)
	}
	return nil
}

// idExists reports whether an id is already present in a v2 index table.
func idExists(ctx context.Context, db *sql.DB, table, id string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("importer: idExists %s: %w", table, err)
	}
	return true, nil
}

// countInsert increments written or skipped based on whether a row was inserted.
func countInsert(res sql.Result, written, skipped *int) {
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		*written++
	} else {
		*skipped++
	}
}

// reformatTime converts a v1 RFC3339 timestamp to the v2 canonical fixed-width
// form so TEXT timestamps sort lexically. Unparseable input is passed through.
func reformatTime(s string) string {
	t, err := core.ParseTime(s)
	if err != nil {
		return s
	}
	return core.FormatTime(t)
}
