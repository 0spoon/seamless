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
const projectCols = `id, slug, name, description, parent_slug, retired_at, created_at, updated_at`

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
	rows, err := db.QueryContext(ctx,
		`SELECT `+projectCols+` FROM projects WHERE slug = ? LIMIT 1`, slug)
	if err != nil {
		return core.Project{}, false, fmt.Errorf("store.ProjectBySlug: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return core.Project{}, false, rows.Err()
	}
	p, err := scanProject(rows)
	if err != nil {
		return core.Project{}, false, fmt.Errorf("store.ProjectBySlug: %w", err)
	}
	return p, true, nil
}

// CreateProject inserts a project. It returns ErrSlugExists if the slug is taken.
func CreateProject(ctx context.Context, db *sql.DB, p core.Project) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO projects (id, slug, name, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, p.Slug, p.Name, p.Description,
		core.FormatTime(p.CreatedAt), core.FormatTime(p.UpdatedAt))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrSlugExists
		}
		return fmt.Errorf("store.CreateProject: %w", err)
	}
	return nil
}

func scanProject(rows *sql.Rows) (core.Project, error) {
	var (
		p                core.Project
		created, updated string
		retired          sql.NullString
	)
	if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.ParentSlug, &retired, &created, &updated); err != nil {
		return core.Project{}, err
	}
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
func SetProjectParent(ctx context.Context, db *sql.DB, slug, parent string, now time.Time) error {
	_, err := db.ExecContext(ctx,
		`UPDATE projects SET parent_slug = ?, updated_at = ? WHERE slug = ?`,
		parent, core.FormatTime(now.UTC()), slug)
	if err != nil {
		return fmt.Errorf("store.SetProjectParent: %w", err)
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
