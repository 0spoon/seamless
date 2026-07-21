-- Favorites: an owner/agent star on an item, surfaced as filters, favorites-first
-- sorts, a briefing pin, and a recall boost. For memories and notes the file
-- frontmatter (favorite: true) is authoritative and these columns are the
-- rebuildable mirror; for the other tables the column is the source of truth.
-- Plans have no table: a plan's favorite is its primary note's flag.
ALTER TABLE memories_index ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0;
ALTER TABLE notes_index    ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects       ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks          ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions       ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0;
ALTER TABLE trials         ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0;
