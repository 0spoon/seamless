-- Backfill the work record into the unified FTS table so `recall` reaches it.
--
-- Until now `fts` held only the file-backed knowledge kinds (memory, note), so
-- everything else an agent had written -- a task's acceptance criteria, a
-- trial's expected-vs-actual, a session's handoff findings -- was reachable only
-- by knowing which list tool to call. The table's columns are already generic
-- (item_id, kind, project, title, name, description, body) and the `kind` column
-- is what scopes a search, so no schema change is needed: only rows.
--
-- Ongoing maintenance lives next to the writers in store/index_work.go; this
-- migration exists to catch up the rows that predate it. The column mapping here
-- MUST match IndexTaskFTS/IndexTrialFTS/IndexSessionFTS -- a mirror whose
-- backfill disagrees with its writer is worse than no backfill, because the
-- disagreement only shows up for old rows.
--
-- The delete-first guards make this safe to re-run against a DB where some rows
-- were already indexed by a newer binary before the migration ran.

DELETE FROM fts WHERE kind IN ('task', 'trial', 'session');

-- Tasks: title + body, with the plan slug in `name` so a plan's steps are
-- findable by composition. Closed tasks are included on purpose -- a dropped
-- task is often the only record of why something was not done.
INSERT INTO fts (item_id, kind, project, title, name, description, body)
    SELECT id, 'task', project_slug, title, plan_slug, '', body FROM tasks;

-- Trials: lab in `name`, outcome in `description`, and the three prose fields
-- joined into the body. TRIM/NULLIF drops the blank fields so a trial that
-- recorded only some of them does not index runs of empty lines.
INSERT INTO fts (item_id, kind, project, title, name, description, body)
    SELECT id, 'trial', project_slug, title, lab, outcome,
           TRIM(
               COALESCE(NULLIF(TRIM(changes), '') || char(10), '') ||
               COALESCE(NULLIF(TRIM(expected), '') || char(10), '') ||
               COALESCE(NULLIF(TRIM(actual), ''), ''),
               char(10)
           )
      FROM trials;

-- Sessions: findings only. A session with no findings (or only the auto-harvest
-- sentinel) says nothing worth searching, and indexing it anyway would put one
-- row in the corpus per process start. The sentinel is spelled out here because
-- a migration is a frozen snapshot; store.SessionFindingsIndexable is the live
-- copy of this predicate.
INSERT INTO fts (item_id, kind, project, title, name, description, body)
    SELECT id, 'session', project_slug, name, '', '', findings
      FROM sessions
     WHERE TRIM(findings) <> ''
       AND TRIM(findings) <> '(auto) session ended, no summary harvested';
