package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/0spoon/seamless/internal/core"
)

// memoryCols is the SELECT list for memories_index, matching scanMemory. Body is
// not in the index (it lives in the file), so a scanned memory has Body == "".
const memoryCols = `id, kind, name, description, project, file_path, tags,
	valid_from, invalid_at, superseded_by, source_session, content_hash,
	created_at, updated_at`

// scanMemory scans one memories_index row (memoryCols order) into a core.Memory.
func scanMemory(rows *sql.Rows) (core.Memory, error) {
	var (
		m                                  core.Memory
		kind, tags, srcSession, hash       string
		created, updated                   string
		validFrom, invalidAt, supersededBy sql.NullString
	)
	if err := rows.Scan(
		&m.ID, &kind, &m.Name, &m.Description, &m.Project, &m.FilePath, &tags,
		&validFrom, &invalidAt, &supersededBy, &srcSession, &hash,
		&created, &updated,
	); err != nil {
		return core.Memory{}, err
	}
	m.Kind = core.MemoryKind(kind)
	m.Tags = decodeTags(tags)
	m.SourceSession = srcSession
	m.ContentHash = hash
	m.SupersededBy = supersededBy.String

	var err error
	if m.ValidFrom, err = nullTime(validFrom); err != nil {
		return core.Memory{}, fmt.Errorf("valid_from: %w", err)
	}
	if m.InvalidAt, err = nullTimePtr(invalidAt); err != nil {
		return core.Memory{}, fmt.Errorf("invalid_at: %w", err)
	}
	if m.Created, err = core.ParseTime(created); err != nil {
		return core.Memory{}, fmt.Errorf("created_at: %w", err)
	}
	if m.Updated, err = core.ParseTime(updated); err != nil {
		return core.Memory{}, fmt.Errorf("updated_at: %w", err)
	}
	return m, nil
}

// scanMemories drains rows into a slice via scanMemory.
func scanMemories(rows *sql.Rows) ([]core.Memory, error) {
	var out []core.Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ActiveMemories returns the active (not superseded/archived) memories visible to
// a project: its own plus all global memories (project == ""). Rows carry index
// metadata only (no body); newest-updated first. Passing project == "" returns
// only global memories.
func ActiveMemories(ctx context.Context, db *sql.DB, project string) ([]core.Memory, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+memoryCols+`
		FROM memories_index
		WHERE invalid_at IS NULL AND (project = ? OR project = '')
		ORDER BY updated_at DESC, id DESC`, project)
	if err != nil {
		return nil, fmt.Errorf("store.ActiveMemories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	mems, err := scanMemories(rows)
	if err != nil {
		return nil, fmt.Errorf("store.ActiveMemories: %w", err)
	}
	return mems, nil
}

// MemoryByName returns the active memory with an exact (project, name), most
// recently updated first. found is false when none matches. It does not fall
// back to the global scope; a caller that wants that resolves it explicitly.
func MemoryByName(ctx context.Context, db *sql.DB, project, name string) (core.Memory, bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+memoryCols+`
		FROM memories_index
		WHERE project = ? AND name = ? AND invalid_at IS NULL
		ORDER BY updated_at DESC, id DESC LIMIT 1`, project, name)
	if err != nil {
		return core.Memory{}, false, fmt.Errorf("store.MemoryByName: %w", err)
	}
	defer func() { _ = rows.Close() }()
	mems, err := scanMemories(rows)
	if err != nil {
		return core.Memory{}, false, fmt.Errorf("store.MemoryByName: %w", err)
	}
	if len(mems) == 0 {
		return core.Memory{}, false, nil
	}
	return mems[0], true, nil
}

// MemoryByID returns the memory with the given id. found is false when absent.
func MemoryByID(ctx context.Context, db *sql.DB, id string) (core.Memory, bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+memoryCols+`
		FROM memories_index WHERE id = ? LIMIT 1`, id)
	if err != nil {
		return core.Memory{}, false, fmt.Errorf("store.MemoryByID: %w", err)
	}
	defer func() { _ = rows.Close() }()
	mems, err := scanMemories(rows)
	if err != nil {
		return core.Memory{}, false, fmt.Errorf("store.MemoryByID: %w", err)
	}
	if len(mems) == 0 {
		return core.Memory{}, false, nil
	}
	return mems[0], true, nil
}

// MemoriesByIDs returns the memories for the given ids in no particular order.
// Missing ids are simply absent from the result; callers key by ID.
func MemoriesByIDs(ctx context.Context, db *sql.DB, ids []string) (map[string]core.Memory, error) {
	out := make(map[string]core.Memory, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, `SELECT `+memoryCols+`
		FROM memories_index WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("store.MemoriesByIDs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	mems, err := scanMemories(rows)
	if err != nil {
		return nil, fmt.Errorf("store.MemoriesByIDs: %w", err)
	}
	for _, m := range mems {
		out[m.ID] = m
	}
	return out, nil
}
