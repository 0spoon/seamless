-- Project topology for the split flagship: two new gardener proposal kinds
-- ('reproject' moves one memory to another project; 'split' is the setup step
-- that creates the child/shared projects, links them as a family, parents the
-- children, and retires the emptied source), plus project parent/retired state.
--
-- SQLite cannot alter a CHECK constraint in place, so gardener_proposals is
-- recreated with the widened kind check and its rows copied over. Nothing
-- references gardener_proposals, so the drop is safe.
CREATE TABLE gardener_proposals_new (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('merge','archive','digest','consolidate','reproject','split')),
    payload     TEXT NOT NULL,                          -- JSON object
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','applied','dismissed')),
    created_at  TEXT NOT NULL,
    resolved_at TEXT
);

INSERT INTO gardener_proposals_new (id, kind, payload, status, created_at, resolved_at)
    SELECT id, kind, payload, status, created_at, resolved_at FROM gardener_proposals;

DROP TABLE gardener_proposals;
ALTER TABLE gardener_proposals_new RENAME TO gardener_proposals;

CREATE INDEX IF NOT EXISTS idx_gardener_status ON gardener_proposals(status, created_at);

-- Project lifecycle: parent_slug points a child project at a shared parent whose
-- active memories are injected into the child's briefing; retired_at marks a
-- project emptied by a split (kept for provenance, never hard-deleted).
ALTER TABLE projects ADD COLUMN parent_slug TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN retired_at  TEXT;

CREATE INDEX IF NOT EXISTS idx_projects_parent ON projects(parent_slug);
