-- Model attribution: which LLM produced a piece of knowledge, stored verbatim
-- as the provider names it (e.g. "claude-fable-5", "gpt-5.5").
-- sessions.model is the live value for the agent; memories_index.model /
-- notes_index.model mirror the frontmatter snapshot stamped at write time.
-- Empty = unknown.
ALTER TABLE sessions       ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE memories_index ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE notes_index    ADD COLUMN model TEXT NOT NULL DEFAULT '';
