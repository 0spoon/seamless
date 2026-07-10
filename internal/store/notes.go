package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/0spoon/seamless/internal/core"
)

// noteCols is the SELECT list for notes_index, matching scanNote. Body is not in
// the index (it lives in the file), so a scanned note has Body == "".
const noteCols = `id, title, slug, description, project, file_path, tags,
	source_url, content_hash, created_at, updated_at`

// scanNote scans one notes_index row (noteCols order) into a core.Note.
func scanNote(rows *sql.Rows) (core.Note, error) {
	var (
		n                core.Note
		tags, hash       string
		created, updated string
		sourceURL        sql.NullString
	)
	if err := rows.Scan(
		&n.ID, &n.Title, &n.Slug, &n.Description, &n.Project, &n.FilePath, &tags,
		&sourceURL, &hash, &created, &updated,
	); err != nil {
		return core.Note{}, err
	}
	n.Tags = decodeTags(tags)
	n.SourceURL = sourceURL.String
	n.ContentHash = hash

	var err error
	if n.Created, err = core.ParseTime(created); err != nil {
		return core.Note{}, fmt.Errorf("created_at: %w", err)
	}
	if n.Updated, err = core.ParseTime(updated); err != nil {
		return core.Note{}, fmt.Errorf("updated_at: %w", err)
	}
	return n, nil
}

// scanNotes drains rows into a slice via scanNote.
func scanNotes(rows *sql.Rows) ([]core.Note, error) {
	var out []core.Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NoteByID returns the note with the given id. found is false when absent.
func NoteByID(ctx context.Context, db *sql.DB, id string) (core.Note, bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+noteCols+`
		FROM notes_index WHERE id = ? LIMIT 1`, id)
	if err != nil {
		return core.Note{}, false, fmt.Errorf("store.NoteByID: %w", err)
	}
	defer func() { _ = rows.Close() }()
	notes, err := scanNotes(rows)
	if err != nil {
		return core.Note{}, false, fmt.Errorf("store.NoteByID: %w", err)
	}
	if len(notes) == 0 {
		return core.Note{}, false, nil
	}
	return notes[0], true, nil
}

// NoteBySlug returns the note with an exact (project, slug). found is false when
// none matches.
func NoteBySlug(ctx context.Context, db *sql.DB, project, slug string) (core.Note, bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+noteCols+`
		FROM notes_index WHERE project = ? AND slug = ? LIMIT 1`, project, slug)
	if err != nil {
		return core.Note{}, false, fmt.Errorf("store.NoteBySlug: %w", err)
	}
	defer func() { _ = rows.Close() }()
	notes, err := scanNotes(rows)
	if err != nil {
		return core.Note{}, false, fmt.Errorf("store.NoteBySlug: %w", err)
	}
	if len(notes) == 0 {
		return core.Note{}, false, nil
	}
	return notes[0], true, nil
}

// NotesByIDs returns the notes for the given ids keyed by ID; missing ids are
// simply absent.
func NotesByIDs(ctx context.Context, db *sql.DB, ids []string) (map[string]core.Note, error) {
	out := make(map[string]core.Note, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, `SELECT `+noteCols+`
		FROM notes_index WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("store.NotesByIDs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	notes, err := scanNotes(rows)
	if err != nil {
		return nil, fmt.Errorf("store.NotesByIDs: %w", err)
	}
	for _, n := range notes {
		out[n.ID] = n
	}
	return out, nil
}
