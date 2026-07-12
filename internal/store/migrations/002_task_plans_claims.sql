-- Plans-as-composition: tasks carry a plan_slug (a plan step) and an atomic
-- claim with a lease. plan_slug='' is an ordinary (non-plan) task; claimed_by
-- holds the owning session ULID and lease_expires_at is when the claim lapses.
ALTER TABLE tasks ADD COLUMN plan_slug        TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN claimed_by       TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN lease_expires_at TEXT;

CREATE INDEX IF NOT EXISTS idx_tasks_plan ON tasks(project_slug, plan_slug);
