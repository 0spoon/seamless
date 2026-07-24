-- New gardener proposal kind 'rekind': change one memory's kind classification
-- in place (same ULID, same project, same body -- only the frontmatter kind and
-- updated stamp change). It backs the constraint-vs-convention corpus migration:
-- reclassification is propose-only, never a silent bulk rewrite.
--
-- SQLite cannot alter a CHECK constraint in place, so gardener_proposals is
-- recreated with the widened kind check and its rows copied over. Nothing
-- references gardener_proposals, so the drop is safe.
CREATE TABLE gardener_proposals_new (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('merge','archive','digest','consolidate','reproject','split','abandon_plan','memory_wanted','tool_error','rekind')),
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
