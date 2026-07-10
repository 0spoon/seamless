-- 001_initial.sql: complete initial schema for Seamless (seam.db).
--
-- PRAGMAs (journal_mode=WAL, foreign_keys=ON, busy_timeout) are set by the Go
-- code via the connection DSN, never here (they are connection-level).
--
-- Files under {data_dir}/memory and {data_dir}/notes are the source of truth
-- for durable knowledge. The *_index tables and the fts virtual table are
-- rebuildable mirrors kept in sync by the files layer + startup reconciliation.

-- ============================================================
-- Projects
-- ============================================================

CREATE TABLE IF NOT EXISTS projects (
    id          TEXT PRIMARY KEY,
    slug        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

-- ============================================================
-- Memory index (mirror of memory-file frontmatter)
-- ============================================================

CREATE TABLE IF NOT EXISTS memories_index (
    id             TEXT PRIMARY KEY,
    kind           TEXT NOT NULL,
    name           TEXT NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    project        TEXT NOT NULL DEFAULT '',            -- '' = global
    file_path      TEXT NOT NULL UNIQUE,
    tags           TEXT NOT NULL DEFAULT '[]',          -- JSON array
    valid_from     TEXT,
    invalid_at     TEXT,                                -- NULL = active
    superseded_by  TEXT,                                -- ULID of replacement
    source_session TEXT NOT NULL DEFAULT '',
    content_hash   TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);

-- Not UNIQUE on (project, name): a superseded memory coexists with its
-- replacement (which may reuse the name) until archived.
CREATE INDEX IF NOT EXISTS idx_memories_project_name ON memories_index(project, name);
CREATE INDEX IF NOT EXISTS idx_memories_project_kind ON memories_index(project, kind);
CREATE INDEX IF NOT EXISTS idx_memories_invalid ON memories_index(invalid_at);

-- ============================================================
-- Notes index (mirror of note-file frontmatter)
-- ============================================================

CREATE TABLE IF NOT EXISTS notes_index (
    id           TEXT PRIMARY KEY,
    title        TEXT NOT NULL,
    slug         TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    project      TEXT NOT NULL DEFAULT '',              -- '' = inbox
    file_path    TEXT NOT NULL UNIQUE,
    tags         TEXT NOT NULL DEFAULT '[]',            -- JSON array
    source_url   TEXT,
    content_hash TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_notes_project ON notes_index(project);
CREATE INDEX IF NOT EXISTS idx_notes_updated ON notes_index(updated_at);

-- ============================================================
-- Unified full-text search over memories + notes
-- ============================================================

-- Self-contained (not external-content) so a single virtual table can index
-- both item kinds; the files layer manages rows directly (INSERT/DELETE by
-- item_id), so no content triggers are needed.
CREATE VIRTUAL TABLE IF NOT EXISTS fts USING fts5(
    item_id UNINDEXED,
    kind UNINDEXED,          -- 'memory' | 'note'
    project UNINDEXED,
    title,
    name,
    description,
    body,
    tokenize = 'porter unicode61'
);

-- ============================================================
-- Embeddings (brute-force cosine; NO vector DB)
-- ============================================================

CREATE TABLE IF NOT EXISTS embeddings (
    item_id    TEXT PRIMARY KEY,
    kind       TEXT NOT NULL,        -- 'memory' | 'note'
    model      TEXT NOT NULL,
    dims       INTEGER NOT NULL,
    vec        BLOB NOT NULL,        -- little-endian float32
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_embeddings_kind ON embeddings(kind);

-- ============================================================
-- Sessions (DB-of-record)
-- ============================================================

CREATE TABLE IF NOT EXISTS sessions (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    project_slug      TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','completed')),
    findings          TEXT NOT NULL DEFAULT '',
    claude_session_id TEXT NOT NULL DEFAULT '',
    cwd               TEXT NOT NULL DEFAULT '',
    source            TEXT NOT NULL DEFAULT '',
    ambient           INTEGER NOT NULL DEFAULT 0,
    metadata          TEXT NOT NULL DEFAULT '{}',       -- JSON object
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status, updated_at);
CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_slug, updated_at);

-- ============================================================
-- Tasks v2 (dependency-aware ready-queue)
-- ============================================================

CREATE TABLE IF NOT EXISTS tasks (
    id           TEXT PRIMARY KEY,
    project_slug TEXT NOT NULL DEFAULT '',
    title        TEXT NOT NULL,
    body         TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','in_progress','done','dropped')),
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    closed_at    TEXT
);

CREATE INDEX IF NOT EXISTS idx_tasks_project_status ON tasks(project_slug, status);

CREATE TABLE IF NOT EXISTS task_deps (
    task_id    TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    depends_on TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, depends_on)
);

CREATE INDEX IF NOT EXISTS idx_task_deps_depends_on ON task_deps(depends_on);

-- ============================================================
-- Trials (research lab; DB-first with queryable metrics)
-- ============================================================

CREATE TABLE IF NOT EXISTS trials (
    id           TEXT PRIMARY KEY,
    lab          TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    changes      TEXT NOT NULL DEFAULT '',
    expected     TEXT NOT NULL DEFAULT '',
    actual       TEXT NOT NULL DEFAULT '',
    outcome      TEXT NOT NULL DEFAULT '',
    metrics      TEXT NOT NULL DEFAULT '{}',            -- JSON object
    session_id   TEXT NOT NULL DEFAULT '',
    project_slug TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_trials_lab ON trials(lab, created_at);
CREATE INDEX IF NOT EXISTS idx_trials_project ON trials(project_slug, created_at);

-- ============================================================
-- Events (append-only log of everything)
-- ============================================================

CREATE TABLE IF NOT EXISTS events (
    id           TEXT PRIMARY KEY,
    ts           TEXT NOT NULL,
    kind         TEXT NOT NULL,
    session_id   TEXT NOT NULL DEFAULT '',
    project_slug TEXT NOT NULL DEFAULT '',
    item_id      TEXT NOT NULL DEFAULT '',
    payload      TEXT NOT NULL DEFAULT '{}'             -- JSON object
);

CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts);
CREATE INDEX IF NOT EXISTS idx_events_kind ON events(kind, ts);
CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id, ts);
CREATE INDEX IF NOT EXISTS idx_events_item ON events(item_id, ts);

-- ============================================================
-- Retrieval stats (maintained from events)
-- ============================================================

CREATE TABLE IF NOT EXISTS retrieval_stats (
    item_id          TEXT PRIMARY KEY,
    inject_count     INTEGER NOT NULL DEFAULT 0,
    read_count       INTEGER NOT NULL DEFAULT 0,
    last_injected_at TEXT,
    last_read_at     TEXT
);

-- ============================================================
-- Gardener proposals
-- ============================================================

CREATE TABLE IF NOT EXISTS gardener_proposals (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('merge','archive','digest')),
    payload     TEXT NOT NULL,                          -- JSON object
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','applied','dismissed')),
    created_at  TEXT NOT NULL,
    resolved_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_gardener_status ON gardener_proposals(status, created_at);

-- ============================================================
-- Settings (repo_project_map, project_families, budgets, toggles)
-- ============================================================

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- ============================================================
-- Jobs (tiny queue for embeds + LLM digests)
-- ============================================================

CREATE TABLE IF NOT EXISTS jobs (
    id         TEXT PRIMARY KEY,
    type       TEXT NOT NULL,
    payload    TEXT NOT NULL DEFAULT '{}',              -- JSON object
    status     TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','done','error')),
    attempts   INTEGER NOT NULL DEFAULT 0,
    run_after  TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_jobs_status_runafter ON jobs(status, run_after);
