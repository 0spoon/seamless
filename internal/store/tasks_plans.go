// Plan rollups: a plan's status is derived from its step tasks, never stored.
package store

import (
	"context"
	"database/sql"
	"fmt"
)

// PlanRollup is the per-plan aggregate the briefing surfaces: Total step tasks,
// how many are Done (closed), InFlight (in_progress), and Claimable (ready).
type PlanRollup struct {
	Slug      string `json:"slug"`
	Total     int    `json:"total"`
	Done      int    `json:"done"`
	InFlight  int    `json:"inFlight"`
	Claimable int    `json:"claimable"`
}

// ActivePlans returns a rollup for each not-yet-complete plan in a project
// (plans whose every step is closed are omitted, like a done stage). Claimable
// counts ready open steps (same readiness rule as ReadyTasksForPlan); the
// plan's status is derived, never stored. One grouped query covers every plan.
func ActivePlans(ctx context.Context, db *sql.DB, project string) ([]PlanRollup, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT plan_slug,
		       COUNT(*),
		       SUM(CASE WHEN status IN ('done','dropped') THEN 1 ELSE 0 END),
		       SUM(CASE WHEN status = 'in_progress' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN status = 'open' AND NOT EXISTS (
		             SELECT 1 FROM task_deps d
		             JOIN tasks b ON b.id = d.depends_on
		             WHERE d.task_id = tasks.id AND b.status IN ('open','in_progress'))
		           THEN 1 ELSE 0 END)
		  FROM tasks
		 WHERE project_slug = ? AND plan_slug <> ''
		 GROUP BY plan_slug
		 ORDER BY plan_slug ASC`, project)
	if err != nil {
		return nil, fmt.Errorf("store.ActivePlans: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var plans []PlanRollup
	for rows.Next() {
		var p PlanRollup
		if err := rows.Scan(&p.Slug, &p.Total, &p.Done, &p.InFlight, &p.Claimable); err != nil {
			return nil, fmt.Errorf("store.ActivePlans: scan: %w", err)
		}
		if p.Done >= p.Total {
			continue // every step closed -> plan complete, not active
		}
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.ActivePlans: %w", err)
	}
	return plans, nil
}
