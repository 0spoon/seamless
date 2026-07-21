// Free-text search over the structured entities -- tasks, sessions, projects,
// plans, trials. Memories and notes are not here: they are indexed in the fts
// table and searched through FTSSearch (fused with semantic hits by
// internal/retrieve). These five have no FTS mirror, so they match with LIKE
// over the one or two columns a human would search by (a task's title, a
// session's name, a project's slug/name). That is a deliberate floor, not a
// stopgap: they are short,
// low-cardinality labels where substring matching is what the observer expects,
// and mirroring them into fts would mean maintaining index rows for high-churn
// state the files layer does not own.
//
// Every query takes the search text as a bound parameter escaped through
// escapeLikePrefix, so a literal % or _ matches itself rather than acting as a
// wildcard.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// likeContains builds the bound argument for a case-insensitive "contains"
// LIKE, with the needle's metacharacters escaped so it matches literally under
// `ESCAPE '\'`.
func likeContains(s string) string {
	return "%" + escapeLikePrefix(s) + "%"
}

// searchLimit floors an unset/negative limit at a sane page size.
func searchLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	return limit
}

// addSearchSince appends an inclusive timestamp predicate for a trusted column
// name. Callers pass only schema constants from this file; user input remains a
// bound argument.
func addSearchSince(sqlStr string, args []any, column string, since time.Time) (string, []any) {
	if since.IsZero() {
		return sqlStr, args
	}
	return sqlStr + " AND " + column + " >= ?", append(args, core.FormatTime(since.UTC()))
}

// SearchTasks returns tasks whose title contains q, newest-updated first. An
// exact id also matches, so pasting a task id from a log finds its task.
func SearchTasks(ctx context.Context, db *sql.DB, q string, limit int) ([]core.Task, error) {
	return searchTasksSince(ctx, db, q, time.Time{}, limit, "store.SearchTasks")
}

// SearchTasksSince is SearchTasks restricted to tasks updated at or after
// since. A zero since keeps the all-time behavior.
func SearchTasksSince(ctx context.Context, db *sql.DB, q string, since time.Time, limit int) ([]core.Task, error) {
	return searchTasksSince(ctx, db, q, since, limit, "store.SearchTasksSince")
}

func searchTasksSince(ctx context.Context, db *sql.DB, q string, since time.Time, limit int, op string) ([]core.Task, error) {
	sqlStr := `SELECT ` + taskCols + ` FROM tasks
		WHERE (title LIKE ? ESCAPE '\' OR id = ?)`
	args := []any{likeContains(q), q}
	sqlStr, args = addSearchSince(sqlStr, args, "updated_at", since)
	sqlStr += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, searchLimit(limit))
	rows, err := db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	tasks, err := scanTasksWithDeps(ctx, db, rows)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return tasks, nil
}

// SearchSessions returns sessions whose name contains q, newest-updated first.
// An exact id also matches.
func SearchSessions(ctx context.Context, db *sql.DB, q string, limit int) ([]core.Session, error) {
	return searchSessionsSince(ctx, db, q, time.Time{}, limit, "store.SearchSessions")
}

// SearchSessionsSince is SearchSessions restricted to sessions updated at or
// after since. A zero since keeps the all-time behavior.
func SearchSessionsSince(ctx context.Context, db *sql.DB, q string, since time.Time, limit int) ([]core.Session, error) {
	return searchSessionsSince(ctx, db, q, since, limit, "store.SearchSessionsSince")
}

func searchSessionsSince(ctx context.Context, db *sql.DB, q string, since time.Time, limit int, op string) ([]core.Session, error) {
	sqlStr := `SELECT ` + sessionCols + ` FROM sessions
		WHERE (name LIKE ? ESCAPE '\' OR id = ?)`
	args := []any{likeContains(q), q}
	sqlStr, args = addSearchSince(sqlStr, args, "updated_at", since)
	sqlStr += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, searchLimit(limit))
	rows, err := db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer func() { _ = rows.Close() }()
	var out []core.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: scan: %w", op, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return out, nil
}

// SearchProjects returns projects whose slug or display name contains q,
// alphabetically by slug (projects are few and stable, so a name order reads
// better than a recency one).
func SearchProjects(ctx context.Context, db *sql.DB, q string, limit int) ([]core.Project, error) {
	return searchProjectsSince(ctx, db, q, time.Time{}, limit, "store.SearchProjects")
}

// SearchProjectsSince is SearchProjects restricted to projects updated at or
// after since. A zero since keeps the all-time behavior.
func SearchProjectsSince(ctx context.Context, db *sql.DB, q string, since time.Time, limit int) ([]core.Project, error) {
	return searchProjectsSince(ctx, db, q, since, limit, "store.SearchProjectsSince")
}

func searchProjectsSince(ctx context.Context, db *sql.DB, q string, since time.Time, limit int, op string) ([]core.Project, error) {
	needle := likeContains(q)
	sqlStr := `SELECT ` + projectCols + ` FROM projects
		WHERE (slug LIKE ? ESCAPE '\' OR name LIKE ? ESCAPE '\')`
	args := []any{needle, needle}
	sqlStr, args = addSearchSince(sqlStr, args, "updated_at", since)
	sqlStr += ` ORDER BY slug LIMIT ?`
	args = append(args, searchLimit(limit))
	rows, err := db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer func() { _ = rows.Close() }()
	var out []core.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: scan: %w", op, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return out, nil
}

// SearchTrials returns trials whose title or lab contains q, newest first. An
// exact id also matches, so pasting a trial id from a log finds its trial.
func SearchTrials(ctx context.Context, db *sql.DB, q string, limit int) ([]core.Trial, error) {
	return searchTrialsSince(ctx, db, q, time.Time{}, limit, "store.SearchTrials")
}

// SearchTrialsSince is SearchTrials restricted to trials created at or after
// since. A zero since keeps the all-time behavior.
func SearchTrialsSince(ctx context.Context, db *sql.DB, q string, since time.Time, limit int) ([]core.Trial, error) {
	return searchTrialsSince(ctx, db, q, since, limit, "store.SearchTrialsSince")
}

func searchTrialsSince(ctx context.Context, db *sql.DB, q string, since time.Time, limit int, op string) ([]core.Trial, error) {
	needle := likeContains(q)
	sqlStr := `SELECT ` + trialCols + ` FROM trials
		WHERE (title LIKE ? ESCAPE '\' OR lab LIKE ? ESCAPE '\' OR id = ?)`
	args := []any{needle, needle, q}
	sqlStr, args = addSearchSince(sqlStr, args, "created_at", since)
	sqlStr += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, searchLimit(limit))
	rows, err := db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer func() { _ = rows.Close() }()
	var out []core.Trial
	for rows.Next() {
		tr, err := scanTrial(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: scan: %w", op, err)
		}
		out = append(out, tr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return out, nil
}

// PlanSearchRow is one plan hit. A plan is a composition, not a table, so a row
// is identified by (Project, Slug); Title is the best label found -- a matching
// note's title, or the slug itself when only tasks carry it.
type PlanSearchRow struct {
	Slug    string
	Project string
	Title   string
	// Favorite is true when any of the plan's tagged notes is favorited. The
	// authoritative flag lives on the plan's primary note, but this query cannot
	// cheaply identify the primary; the two only disagree after deliberate
	// hand-editing of a secondary note's frontmatter.
	Favorite bool
	Updated  time.Time
}

// SearchPlans returns plans whose slug or narrative-note title contains q,
// newest-updated first.
//
// Plans are bi-sourced (a note tagged plan:<slug>, and tasks carrying
// plan_slug), and either source alone can carry a match: a plan whose steps
// exist but whose note does not, or vice versa. Both are queried and merged
// deduped by (project, slug), keeping the newer Updated and preferring a note's
// title over the slug fallback -- the same merge the Plans screen does, done
// here so a search hit cannot disagree with the page it links to.
func SearchPlans(ctx context.Context, db *sql.DB, q string, limit int) ([]PlanSearchRow, error) {
	return searchPlansSince(ctx, db, q, time.Time{}, limit, "store.SearchPlans")
}

// SearchPlansSince is SearchPlans restricted to matching plan sources updated
// at or after since. Plans are merged before the bound is applied, so either a
// matching narrative or matching task can keep the composition in-window.
func SearchPlansSince(ctx context.Context, db *sql.DB, q string, since time.Time, limit int) ([]PlanSearchRow, error) {
	return searchPlansSince(ctx, db, q, since, limit, "store.SearchPlansSince")
}

func searchPlansSince(ctx context.Context, db *sql.DB, q string, since time.Time, limit int, op string) ([]PlanSearchRow, error) {
	lim := searchLimit(limit)
	needle := likeContains(q)

	// Notes: a plan:<slug> tag, matched on the note's title or on the tag's own
	// slug suffix. json_each is the tag-array reader NotesByTagPrefix uses.
	noteRows, err := db.QueryContext(ctx, `
		SELECT je.value, n.project, n.title, n.favorite, n.updated_at
		FROM notes_index n, json_each(n.tags) je
		WHERE je.value LIKE 'plan:%' ESCAPE '\'
		  AND (n.title LIKE ? ESCAPE '\' OR je.value LIKE ? ESCAPE '\')
		ORDER BY n.updated_at DESC, n.id DESC`,
		needle, needle)
	if err != nil {
		return nil, fmt.Errorf("%s: notes: %w", op, err)
	}
	byKey := make(map[string]*PlanSearchRow)
	var order []string
	upsert := func(project, slug, title string, favorite bool, updated time.Time) {
		if slug == "" {
			return
		}
		key := project + "\x00" + slug
		row, ok := byKey[key]
		if !ok {
			byKey[key] = &PlanSearchRow{Slug: slug, Project: project, Title: title, Favorite: favorite, Updated: updated}
			order = append(order, key)
			return
		}
		if updated.After(row.Updated) {
			row.Updated = updated
		}
		row.Favorite = row.Favorite || favorite
		// A note title is a real label; the task path can only offer the slug.
		if row.Title == row.Slug && title != "" {
			row.Title = title
		}
	}
	err = func() error {
		defer func() { _ = noteRows.Close() }()
		for noteRows.Next() {
			var tag, project, title, updated string
			var favorite bool
			if err := noteRows.Scan(&tag, &project, &title, &favorite, &updated); err != nil {
				return fmt.Errorf("scan: %w", err)
			}
			u, err := core.ParseTime(updated)
			if err != nil {
				return fmt.Errorf("updated_at: %w", err)
			}
			slug := planSlugFromTag(tag)
			if title == "" {
				title = slug
			}
			upsert(project, slug, title, favorite, u)
		}
		return noteRows.Err()
	}()
	if err != nil {
		return nil, fmt.Errorf("%s: notes: %w", op, err)
	}

	// Tasks: the plan_slug column itself.
	taskRows, err := db.QueryContext(ctx, `
		SELECT plan_slug, project_slug, MAX(updated_at) FROM tasks
		WHERE plan_slug != '' AND plan_slug LIKE ? ESCAPE '\'
		GROUP BY plan_slug, project_slug`, needle)
	if err != nil {
		return nil, fmt.Errorf("%s: tasks: %w", op, err)
	}
	err = func() error {
		defer func() { _ = taskRows.Close() }()
		for taskRows.Next() {
			var slug, project, updated string
			if err := taskRows.Scan(&slug, &project, &updated); err != nil {
				return fmt.Errorf("scan: %w", err)
			}
			u, err := core.ParseTime(updated)
			if err != nil {
				return fmt.Errorf("updated_at: %w", err)
			}
			upsert(project, slug, slug, false, u)
		}
		return taskRows.Err()
	}()
	if err != nil {
		return nil, fmt.Errorf("%s: tasks: %w", op, err)
	}

	out := make([]PlanSearchRow, 0, len(order))
	for _, key := range order {
		row := *byKey[key]
		if !since.IsZero() && row.Updated.Before(since) {
			continue
		}
		out = append(out, row)
	}
	sortPlanSearchRows(out)
	if len(out) > lim {
		out = out[:lim]
	}
	return out, nil
}

// sortPlanSearchRows orders merged plan rows newest-updated first, ties broken
// by (project, slug) so a merge over two unordered sources is deterministic.
func sortPlanSearchRows(rows []PlanSearchRow) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if !a.Updated.Equal(b.Updated) {
			return a.Updated.After(b.Updated)
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		return a.Slug < b.Slug
	})
}

// planSlugFromTag strips the "plan:" prefix from a tag value. It duplicates
// nothing from internal/plans on purpose: store must not import a package that
// imports store.
func planSlugFromTag(tag string) string {
	const prefix = "plan:"
	if len(tag) <= len(prefix) || tag[:len(prefix)] != prefix {
		return ""
	}
	return tag[len(prefix):]
}
