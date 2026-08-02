-- New gardener proposal kind 'relocate': when a project tightens to
-- confidential or sealed, knowledge may ALREADY have left the fence -- global
-- memories written by sessions bound to that project. The provenance audit
-- files one relocate proposal per such memory, offering to move it back inside
-- the project. It is a distinct kind from 'reproject' because the evidence and
-- the decision differ: a reproject corrects a mis-scoped memory, a relocate
-- repairs a leak the owner just fenced against, and the inbox has to say which.
--
-- SQLite cannot alter a CHECK constraint in place, so gardener_proposals is
-- recreated with the widened kind check and its rows copied over. Nothing
-- references gardener_proposals, so the drop is safe. The `result` column
-- (migration 018, the undo handle) is carried across with them.
CREATE TABLE gardener_proposals_new (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('merge','archive','digest','consolidate','reproject','split','abandon_plan','memory_wanted','tool_error','rekind','ship_plan','relocate')),
    payload     TEXT NOT NULL,                          -- JSON object
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','applied','dismissed')),
    created_at  TEXT NOT NULL,
    resolved_at TEXT,
    result      TEXT                                    -- JSON object; what an apply produced
);

INSERT INTO gardener_proposals_new (id, kind, payload, status, created_at, resolved_at, result)
    SELECT id, kind, payload, status, created_at, resolved_at, result FROM gardener_proposals;

DROP TABLE gardener_proposals;
ALTER TABLE gardener_proposals_new RENAME TO gardener_proposals;

CREATE INDEX IF NOT EXISTS idx_gardener_status ON gardener_proposals(status, created_at);
