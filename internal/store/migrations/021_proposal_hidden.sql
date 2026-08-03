-- New gardener proposal status 'hidden': rejecting a proposal now has two
-- strengths. A regular 'dismissed' suppresses the pattern only while no NEW
-- evidence arrives -- the next occurrence after the dismissal re-raises it --
-- while 'hidden' is the old forever semantics, kept as a deliberate choice
-- rather than the only one. It is a distinct status rather than a flag so the
-- suppression rule reads off one column, and so the console can list what is
-- hidden and offer to unhide it (which demotes the row back to 'dismissed').
--
-- SQLite cannot alter a CHECK constraint in place, so gardener_proposals is
-- recreated with the widened status check and its rows copied over. Nothing
-- references gardener_proposals, so the drop is safe. The `result` column
-- (migration 018, the undo handle) and the kind check (migration 020) are
-- carried across unchanged.
CREATE TABLE gardener_proposals_new (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('merge','archive','digest','consolidate','reproject','split','abandon_plan','memory_wanted','tool_error','rekind','ship_plan','relocate')),
    payload     TEXT NOT NULL,                          -- JSON object
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','applied','dismissed','hidden')),
    created_at  TEXT NOT NULL,
    resolved_at TEXT,
    result      TEXT                                    -- JSON object; what an apply produced
);

INSERT INTO gardener_proposals_new (id, kind, payload, status, created_at, resolved_at, result)
    SELECT id, kind, payload, status, created_at, resolved_at, result FROM gardener_proposals;

DROP TABLE gardener_proposals;
ALTER TABLE gardener_proposals_new RENAME TO gardener_proposals;

CREATE INDEX IF NOT EXISTS idx_gardener_status ON gardener_proposals(status, created_at);
