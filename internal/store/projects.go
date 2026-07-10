package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/0spoon/seamless/internal/core"
)

// ErrSlugExists is returned by CreateProject when the slug is already taken.
var ErrSlugExists = errors.New("store: project slug already exists")

// ListProjects returns every project, ordered by slug.
func ListProjects(ctx context.Context, db *sql.DB) ([]core.Project, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, slug, name, description, created_at, updated_at
		 FROM projects ORDER BY slug`)
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
		`SELECT id, slug, name, description, created_at, updated_at
		 FROM projects WHERE slug = ? LIMIT 1`, slug)
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
	)
	if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &created, &updated); err != nil {
		return core.Project{}, err
	}
	var err error
	if p.CreatedAt, err = core.ParseTime(created); err != nil {
		return core.Project{}, fmt.Errorf("created_at: %w", err)
	}
	if p.UpdatedAt, err = core.ParseTime(updated); err != nil {
		return core.Project{}, fmt.Errorf("updated_at: %w", err)
	}
	return p, nil
}
