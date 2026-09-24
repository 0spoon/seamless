-- New gardener proposal kind 'merge_plans': the third settlement the stale-plan
-- pass can propose for a captured Claude Code plan that was never approved.
-- Abandon says nothing came of it and ship says the work landed anyway;
-- merge_plans says the work landed under a DIFFERENT composition that already
-- carries the steps, and folds the capture's notes onto that plan's slug.
--
-- It is a distinct kind rather than a flavour of abandon because it is the only
-- plan settlement that MOVES anything: abandon and ship retag one note in
-- place, while this one relocates a whole composition and therefore needs its
-- own apply, its own undo (the result records exactly which notes moved), and
-- its own row in the console inbox.
--
-- SQLite cannot alter a CHECK constraint in place, so gardener_proposals is
-- recreated with the widened kind check and its rows copied over. Nothing
-- references gardener_proposals, so the drop is safe. The status check
-- (migration 021) and the `result` column (migration 018) carry across
-- unchanged.
CREATE TABLE gardener_proposals_new (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('merge','archive','digest','consolidate','reproject','split','abandon_plan','memory_wanted','tool_error','rekind','ship_plan','relocate','merge_plans')),
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
