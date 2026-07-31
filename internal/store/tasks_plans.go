// Plan rollups: a plan's status is derived from its step tasks, never stored.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// PlanRollup is the per-plan aggregate the briefing surfaces: Total step tasks,
// how many are Done (closed), InFlight (in_progress), and Claimable (ready).
// LastActivity is the newest updated_at across the plan's steps.
type PlanRollup struct {
	Slug         string    `json:"slug"`
	Total        int       `json:"total"`
	Done         int       `json:"done"`
	InFlight     int       `json:"inFlight"`
	Claimable    int       `json:"claimable"`
	LastActivity time.Time `json:"lastActivity"`
}

// ActivePlans returns a rollup for each not-yet-complete plan in a project
// (plans whose every step is closed are omitted, like a done stage). Claimable
// counts ready open steps (same readiness rule as ReadyTasksForPlan); the
// plan's status is derived, never stored. One grouped query covers every plan.
// Ordered most-recently-active first (newest step updated_at), ties broken by
// slug, so the briefing and the console lead with what just moved.
//
// This is a briefing contract: dropping the complete plans is the whole point,
// so the filter lives here rather than in the shared query.
func ActivePlans(ctx context.Context, db *sql.DB, project string) ([]PlanRollup, error) {
	all, err := planRollups(ctx, db, project)
	if err != nil {
		return nil, err
	}
	var plans []PlanRollup
	for _, p := range all {
		if p.Done >= p.Total {
			continue // every step closed -> plan complete, not active
		}
		plans = append(plans, p)
	}
	return plans, nil
}

// PlanRollupsForProject returns a rollup for EVERY plan in a project, complete
// or not, in the same order. The console's plan workspace partitions active
// from completed itself (a finished plan still gets a rollup line); ActivePlans
// is this set with the complete plans dropped, so the two cannot disagree.
func PlanRollupsForProject(ctx context.Context, db *sql.DB, project string) ([]PlanRollup, error) {
	return planRollups(ctx, db, project)
}

// planRollups is the shared grouped query behind both plan rollup views.
func planRollups(ctx context.Context, db *sql.DB, project string) ([]PlanRollup, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT plan_slug,
		       COUNT(*),
		       SUM(CASE WHEN status IN ('done','dropped') THEN 1 ELSE 0 END),
		       SUM(CASE WHEN status = 'in_progress' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN status = 'open' AND NOT EXISTS (
		             SELECT 1 FROM task_deps d
		             JOIN tasks b ON b.id = d.depends_on
		             WHERE d.task_id = tasks.id AND b.status IN ('open','in_progress'))
		           THEN 1 ELSE 0 END),
		       MAX(updated_at)
		  FROM tasks
		 WHERE project_slug = ? AND plan_slug <> ''
		 GROUP BY plan_slug
		 ORDER BY MAX(updated_at) DESC, plan_slug ASC`, project)
	if err != nil {
		return nil, fmt.Errorf("store.planRollups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var plans []PlanRollup
	for rows.Next() {
		var p PlanRollup
		var last string
		if err := rows.Scan(&p.Slug, &p.Total, &p.Done, &p.InFlight, &p.Claimable, &last); err != nil {
			return nil, fmt.Errorf("store.planRollups: scan: %w", err)
		}
		if p.LastActivity, err = core.ParseTime(last); err != nil {
			return nil, fmt.Errorf("store.planRollups: parse last activity: %w", err)
		}
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.planRollups: %w", err)
	}
	return plans, nil
}
