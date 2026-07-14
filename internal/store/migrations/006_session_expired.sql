-- New session status 'expired': the gardener reaper closes a session that went
-- idle past the liveness TTL without a graceful session_end (a crashed/killed
-- agent, or an explicit session_start whose agent never ended it). It is distinct
-- from 'completed' so the console can tell a harvested session from an abandoned
-- one.
--
-- SQLite cannot alter a CHECK constraint in place, so sessions is recreated with
-- the widened status check and its rows copied over. Nothing has a foreign key to
-- sessions, so the drop is safe.
CREATE TABLE sessions_new (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    project_slug      TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','completed','expired')),
    findings          TEXT NOT NULL DEFAULT '',
    claude_session_id TEXT NOT NULL DEFAULT '',
    cwd               TEXT NOT NULL DEFAULT '',
    source            TEXT NOT NULL DEFAULT '',
    ambient           INTEGER NOT NULL DEFAULT 0,
    metadata          TEXT NOT NULL DEFAULT '{}',       -- JSON object
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);

INSERT INTO sessions_new (id, name, project_slug, status, findings, claude_session_id,
                          cwd, source, ambient, metadata, created_at, updated_at)
    SELECT id, name, project_slug, status, findings, claude_session_id,
           cwd, source, ambient, metadata, created_at, updated_at FROM sessions;

DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;

CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status, updated_at);
CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_slug, updated_at);
