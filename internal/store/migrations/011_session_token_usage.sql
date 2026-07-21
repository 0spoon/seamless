-- Real model token consumption harvested from the agent's transcript at session
-- end (Claude Code) or every turn (Codex Stop), stored verbatim as absolute
-- cumulative totals -- NOT the ~4 bytes/token injection estimates. Paired with
-- the injected-context estimate (retrieval.injected events), input+cached+output
-- gives Seamless's share of a session's real context spend.
--
-- The fields are normalized across clients so a per-project SUM is meaningful:
--   input_tokens          fresh (uncached) input the model processed
--   cached_input_tokens   input served from the prompt cache (read)
--   cache_creation_tokens input written to the prompt cache (Claude Code only; 0 for Codex)
--   output_tokens         tokens the model produced
--   total_tokens          input + cached_input + cache_creation + output (all tokens billed)
--
-- Every value is a cumulative absolute total for the session, OVERWRITTEN on each
-- harvest (never accumulated), so re-harvesting a resumed/compacted session's
-- grown transcript cannot double-count. Empty (0) = never harvested, or a client
-- with no transcript token record.
ALTER TABLE sessions ADD COLUMN input_tokens          INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN cached_input_tokens   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN cache_creation_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN output_tokens         INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN total_tokens          INTEGER NOT NULL DEFAULT 0;
