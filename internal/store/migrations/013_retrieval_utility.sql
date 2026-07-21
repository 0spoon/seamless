-- Utility: time-decayed demand score derived from the event log by
-- RebuildRetrievalStats. utility is the normalized [0,1) score; utility_components
-- is a JSON breakdown of the decayed raw sums per signal class
-- ({"read":x,"recall":y,"prompt":z}) for the console peek. Both are rebuildable
-- projections -- the events table stays the source of truth.
ALTER TABLE retrieval_stats ADD COLUMN utility REAL NOT NULL DEFAULT 0;
ALTER TABLE retrieval_stats ADD COLUMN utility_components TEXT;
