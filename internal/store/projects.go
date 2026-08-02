package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// ErrSlugExists is returned by CreateProject when the slug is already taken.
var ErrSlugExists = errors.New("store: project slug already exists")

// projectCols is the SELECT list for the projects table, matching scanProject.
const projectCols = `id, slug, name, description, parent_slug, retired_at, favorite, isolation, created_at, updated_at`

// EnsureProject returns the project registered under slug, creating a minimal
// row when none exists yet. It is the idempotent upsert used by the importer and
// by session resolution so that every project referenced by memories, notes, or
// sessions also has a first-class projects-table row -- the row project_list
// reads. A blank slug is the global scope: it is never registered and yields the
// zero Project with no error. When name is blank the slug is used as the name.
func EnsureProject(ctx context.Context, db *sql.DB, slug, name string) (core.Project, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return core.Project{}, nil
	}
	if p, ok, err := ProjectBySlug(ctx, db, slug); err != nil || ok {
		return p, err
	}
	if strings.TrimSpace(name) == "" {
		name = slug
	}
	id, err := core.NewID()
	if err != nil {
		return core.Project{}, fmt.Errorf("store.EnsureProject: %w", err)
	}
	now := time.Now().UTC()
	p := core.Project{ID: id, Slug: slug, Name: name, CreatedAt: now, UpdatedAt: now}
	if err := CreateProject(ctx, db, p); err != nil {
		if errors.Is(err, ErrSlugExists) {
			// Lost a create race with a concurrent caller; return the winner's row.
			if got, ok, gerr := ProjectBySlug(ctx, db, slug); gerr == nil && ok {
				return got, nil
			}
		}
		return core.Project{}, err
	}
	return p, nil
}

// ListProjects returns every project, ordered by slug.
func ListProjects(ctx context.Context, db *sql.DB) ([]core.Project, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT `+projectCols+` FROM projects ORDER BY slug`)
	if err != nil {
		return nil, fmt.Errorf("store.ListProjects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []core.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("store.ListProjects: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProjectBySlug returns the project with the given slug. found is false when
// absent.
func ProjectBySlug(ctx context.Context, db *sql.DB, slug string) (core.Project, bool, error) {
	p, ok, err := projectBySlugTx(ctx, db, slug)
	if err != nil {
		return core.Project{}, false, fmt.Errorf("store.ProjectBySlug: %w", err)
	}
	return p, ok, nil
}

// projectBySlugTx loads one project row via any read executor, so a mutator can
// re-read the row inside the transaction it is about to write under.
func projectBySlugTx(ctx context.Context, q rowQuerier, slug string) (core.Project, bool, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+projectCols+` FROM projects WHERE slug = ? LIMIT 1`, slug)
	if err != nil {
		return core.Project{}, false, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return core.Project{}, false, rows.Err()
	}
	p, err := scanProject(rows)
	if err != nil {
		return core.Project{}, false, err
	}
	return p, true, nil
}

// CreateProject inserts a project. It returns ErrSlugExists if the slug is taken.
// A zero Isolation is stored as open (the default); a present-but-unrecognized
// state is an error, never silently defaulted.
func CreateProject(ctx context.Context, db *sql.DB, p core.Project) error {
	iso := p.Isolation
	if iso == "" {
		iso = core.IsolationOpen
	}
	if !iso.Valid() {
		return fmt.Errorf("store.CreateProject: invalid isolation %q", p.Isolation)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO projects (id, slug, name, description, isolation, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Slug, p.Name, p.Description, string(iso),
		core.FormatTime(p.CreatedAt), core.FormatTime(p.UpdatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return ErrSlugExists
		}
		return fmt.Errorf("store.CreateProject: %w", err)
	}
	return nil
}

func scanProject(rows *sql.Rows) (core.Project, error) {
	var (
		p                           core.Project
		created, updated, isolation string
		retired                     sql.NullString
	)
	if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.ParentSlug, &retired, &p.Favorite, &isolation, &created, &updated); err != nil {
		return core.Project{}, err
	}
	p.Isolation = core.Isolation(isolation)
	var err error
	if p.RetiredAt, err = nullTimePtr(retired); err != nil {
		return core.Project{}, fmt.Errorf("retired_at: %w", err)
	}
	if p.CreatedAt, err = core.ParseTime(created); err != nil {
		return core.Project{}, fmt.Errorf("created_at: %w", err)
	}
	if p.UpdatedAt, err = core.ParseTime(updated); err != nil {
		return core.Project{}, fmt.Errorf("updated_at: %w", err)
	}
	return p, nil
}

// SetProjectParent sets (or clears, with parent == "") a project's parent slug
// and bumps updated_at. The parent's active memories are injected into the
// child's briefing (see retrieve.Briefing). It is idempotent -- re-setting the
// same parent is a harmless no-op write -- so a split apply is retry-safe. An
// unknown slug affects no rows and returns nil (the caller ensures the row first).
//
// ATTACHING refuses when EITHER side is isolated (ErrIsolationStandalone).
// Isolation requires a standalone project, and a parent link is a briefing
// cross-over surface, so a fenced project may neither take a parent nor be one.
// This is the link-side half of the rule TightenProjectIsolation enforces from
// the tighten side (it detaches the parent, and refuses outright while children
// remain, ErrIsolationHasChildren).
//
// The guard lives at the store call rather than at each caller -- unlike the
// family writers, which are guarded in the console and the CLI so a split apply
// cannot fail inside the store -- because SetProjectParent's only non-test
// caller is that split apply, and gardener.Split already refuses a fenced source
// with its own ErrIsolatedProject. The refusal is therefore unreachable on that
// path and cannot regress it; if a future proposal ever did name a fenced slug,
// gardener.Apply surfaces the error as a console flash, not a 500.
//
// DETACHING (a blank parent) is always allowed, an isolated child included:
// clearing a parent moves TOWARD the standalone rule, and refusing it would
// strand a project that acquired a fence and a parent by some other route (a
// legacy row, an import) with no way to comply. TightenProjectIsolation does not
// route through here -- it detaches inline in its own transaction -- so nothing
// about the tighten depends on this decision either way.
//
// The check and the UPDATE share one transaction so a concurrent tighten cannot
// land between them and leave a freshly fenced project holding a parent link.
func SetProjectParent(ctx context.Context, db *sql.DB, slug, parent string, now time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store.SetProjectParent: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	if strings.TrimSpace(parent) != "" {
		blocked, err := isolatedSlugsTx(ctx, tx, []string{slug, parent})
		if err != nil {
			return fmt.Errorf("store.SetProjectParent: %w", err)
		}
		if len(blocked) > 0 {
			return fmt.Errorf("store.SetProjectParent: %w: %s -- an isolated project may neither take a parent nor be one; set isolation back to open first",
				ErrIsolationStandalone, isolatedLabels(blocked))
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET parent_slug = ?, updated_at = ? WHERE slug = ?`,
		parent, core.FormatTime(now.UTC()), slug); err != nil {
		return fmt.Errorf("store.SetProjectParent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store.SetProjectParent: commit: %w", err)
	}
	return nil
}

// RetireProject stamps a project's retired_at (marking it emptied by a split) and
// bumps updated_at. Passing the zero time clears it (un-retire). It is idempotent
// and leaves the project's rows and files intact -- retirement is a flag, never a
// delete. An unknown slug affects no rows and returns nil.
func RetireProject(ctx context.Context, db *sql.DB, slug string, at time.Time) error {
	updated := at
	var retiredAt any
	if !at.IsZero() {
		retiredAt = core.FormatTime(at.UTC())
	} else {
		updated = time.Now()
	}
	_, err := db.ExecContext(ctx,
		`UPDATE projects SET retired_at = ?, updated_at = ? WHERE slug = ?`,
		retiredAt, core.FormatTime(updated.UTC()), slug)
	if err != nil {
		return fmt.Errorf("store.RetireProject: %w", err)
	}
	return nil
}
