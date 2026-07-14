-- One-off repair for tasks stuck from before UpdateTask cleared claim fields on
-- reopen: a task moved back to status='open' kept claimed_by/lease_expires_at,
-- and ClaimTask's compare-and-set at the time required claimed_by='' on open
-- tasks, leaving the row visible in the ready queue yet permanently
-- unclaimable. Both bugs are fixed in code (reopen clears the claim; ClaimTask
-- ignores stale claims on open rows), so this normalizes legacy rows to the
-- invariant that an open task carries no claim and no lease. Idempotent: rows
-- already clean do not match the WHERE clause. updated_at is left untouched --
-- this is a repair, not an edit.
UPDATE tasks
   SET claimed_by = '', lease_expires_at = NULL
 WHERE status = 'open' AND (claimed_by <> '' OR lease_expires_at IS NOT NULL);
