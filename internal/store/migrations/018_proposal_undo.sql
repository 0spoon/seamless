-- Applying a proposal returns a per-kind result map (note_id, task_id, moved
-- refs...) that until now was handed to the caller and dropped. Undo needs it:
-- inverting a digest means finding the note the apply wrote, inverting a
-- memory_wanted means finding the task it opened. Persisting the result next to
-- the proposal is what makes an applied proposal reversible without guessing
-- which artifact belongs to it.
--
-- A plain ADD COLUMN suffices (no CHECK constraint changes), so unlike the kind
-- migrations this one does not recreate the table.
ALTER TABLE gardener_proposals ADD COLUMN result TEXT;
