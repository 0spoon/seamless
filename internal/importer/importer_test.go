package importer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/store"
)

// writeV1 lays out a synthetic v1 source dir (notes tree + seam.db) and returns
// its path.
func writeV1(t *testing.T) string {
	t.Helper()
	src := t.TempDir()

	files := map[string]string{
		"agent-memory/knowledge-gotcha-thing.md": `---
id: 01MEM0000000000000000000AA
title: 'Knowledge: gotcha - a tricky thing'
description: 'a one-line description'
project: agent-memory
tags: ['created-by:agent', 'domain:gotcha', 'project:seam', 'type:knowledge']
created: 2026-05-22T21:43:31Z
modified: 2026-07-09T18:46:48Z
---
The tricky thing is that boot order matters.
`,
		"research/trial-gatt.md": `---
id: 01TRIAL000000000000000000A
title: 'Trial: GATT go/no-go'
project: research
tags: ['domain:firmware', 'lab:mw75-firmware-ble', 'type:trial']
created: 2026-07-07T23:25:00Z
modified: 2026-07-08T00:27:49Z
---
**Lab:** mw75-firmware-ble
**Outcome:** pending

## Changes
did a thing

## Expected
should work

## Actual
did not work
`,
		"seam/landscape-scan.md": `---
id: 01NOTE0000000000000000000A
title: 'Landscape scan'
description: 'a survey'
project: seam
tags: ['created-by:agent']
created: 2026-06-18T20:30:31Z
modified: 2026-06-18T21:00:00Z
---
# Findings

Mem0 delta ~2%.
`,
		"briefings/2026-07-10.md": `---
id: 01BRIEF000000000000000000A
title: 'Briefing 2026-07-10'
project: briefings
tags: ['type:briefing']
created: 2026-07-10T09:00:00Z
modified: 2026-07-10T09:00:00Z
---
ephemeral briefing content
`,
	}
	for rel, content := range files {
		abs := filepath.Join(src, "notes", filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
	}

	writeV1DB(t, filepath.Join(src, "seam.db"))
	return src
}

// writeV1DB creates a minimal v1 seam.db with agent_sessions + agent_tool_calls.
func writeV1DB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	_, err = db.Exec(`
		CREATE TABLE agent_sessions (
		    id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, status TEXT, findings TEXT,
		    metadata TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
		CREATE TABLE agent_tool_calls (
		    id TEXT PRIMARY KEY, session_id TEXT, tool_name TEXT NOT NULL, arguments TEXT,
		    result TEXT, error TEXT, duration_ms INTEGER, created_at TEXT NOT NULL);`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO agent_sessions VALUES
		('01SESS0000000000000000000A','mw75-firmware-ble','completed','found the mcu','{}','2026-06-01T10:00:00Z','2026-06-01T12:00:00Z'),
		('01SESS0000000000000000000B','stale-session','active','','{"k":"v"}','2026-06-02T10:00:00Z','2026-06-02T11:00:00Z')`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO agent_tool_calls VALUES
		('01TC00000000000000000000AA','01SESS0000000000000000000A','memory_write','{}','ok',NULL,42,'2026-06-01T10:05:00Z'),
		('01TC00000000000000000000BB',NULL,'notes_read','{}',NULL,'boom',7,'2026-06-01T10:06:00Z')`)
	require.NoError(t, err)
}

func newV2(t *testing.T) (*files.Manager, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	m, err := files.NewManager(dir, db, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	return m, db
}

func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n))
	return n
}

func TestImportEndToEnd(t *testing.T) {
	src := writeV1(t)
	mgr, db := newV2(t)
	ctx := context.Background()

	rep, err := Import(ctx, mgr, db, Options{SourceDir: src, SkipProjects: []string{"briefings"}})
	require.NoError(t, err)

	require.Equal(t, 1, rep.Memories)
	require.Equal(t, 1, rep.Notes)
	require.Equal(t, 1, rep.Trials)
	require.Equal(t, 2, rep.Sessions)
	require.Equal(t, 2, rep.Events)

	// Memory landed with decoded kind/name/project.
	var kind, name, project string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT kind, name, project FROM memories_index WHERE id='01MEM0000000000000000000AA'`).
		Scan(&kind, &name, &project))
	require.Equal(t, "gotcha", kind)
	require.Equal(t, "a-tricky-thing", name)
	require.Equal(t, "seam", project)

	// Memory file exists on disk under memory/seam/.
	_, err = os.Stat(filepath.Join(mgr.Store().DataDir(), "memory", "seam", "a-tricky-thing.md"))
	require.NoError(t, err)

	// Briefings project was skipped entirely.
	require.Equal(t, 1, count(t, db, "notes_index")) // only landscape-scan, not the briefing

	// Trial row parsed.
	var lab, outcome, changes string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT lab, outcome, changes FROM trials WHERE id='01TRIAL000000000000000000A'`).
		Scan(&lab, &outcome, &changes))
	require.Equal(t, "mw75-firmware-ble", lab)
	require.Equal(t, "pending", outcome)
	require.Equal(t, "did a thing", changes)

	// Session status coercion: the stale "active" v1 row becomes completed.
	var status string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT status FROM sessions WHERE id='01SESS0000000000000000000B'`).Scan(&status))
	require.Equal(t, "completed", status)

	// Tool call became a tool.call event with the session linked and payload set.
	var evKind, sessID, payload string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT kind, session_id, payload FROM events WHERE id='01TC00000000000000000000AA'`).
		Scan(&evKind, &sessID, &payload))
	require.Equal(t, "tool.call", evKind)
	require.Equal(t, "01SESS0000000000000000000A", sessID)
	require.Contains(t, payload, "memory_write")

	// The tool call with a NULL session imported with an empty session_id.
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT session_id FROM events WHERE id='01TC00000000000000000000BB'`).Scan(&sessID))
	require.Empty(t, sessID)
}

// Re-running the import must not duplicate anything (idempotent by id).
func TestImportIdempotent(t *testing.T) {
	src := writeV1(t)
	mgr, db := newV2(t)
	ctx := context.Background()

	_, err := Import(ctx, mgr, db, Options{SourceDir: src, SkipProjects: []string{"briefings"}})
	require.NoError(t, err)

	rep2, err := Import(ctx, mgr, db, Options{SourceDir: src, SkipProjects: []string{"briefings"}})
	require.NoError(t, err)
	require.Zero(t, rep2.Memories)
	require.Zero(t, rep2.Notes)
	require.Zero(t, rep2.Trials)
	require.Zero(t, rep2.Sessions)
	require.Zero(t, rep2.Events)
	require.Equal(t, 7, rep2.Skipped) // 1 mem + 1 note + 1 trial + 2 sessions + 2 events

	// Totals unchanged after the second run.
	require.Equal(t, 1, count(t, db, "memories_index"))
	require.Equal(t, 1, count(t, db, "notes_index"))
	require.Equal(t, 1, count(t, db, "trials"))
	require.Equal(t, 2, count(t, db, "sessions"))
	require.Equal(t, 2, count(t, db, "events"))
}
