package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/0spoon/seamless/internal/core"
)

// PlanRef identifies one plan by the (project, slug) pair. Plan slugs are
// unique only PER PROJECT (see DistinctPlanSlugsForProject), so a cross-project
// rollup must key on the pair, never the slug alone.
type PlanRef struct {
	Project string
	Slug    string
}

// PlanTokenRollup is one plan's cumulative model-token attribution: the
// harvested transcript totals (migration 011) of every session that worked or
// authored the plan.
//
// Attribution is deliberately session-grained and whole-session: tokens exist
// only per session, so the honest plan-level number is "the token totals of the
// sessions attributed to this plan" -- never an invented per-step split. A
// session working several steps of the same plan counts once.
type PlanTokenRollup struct {
	// Sessions is the distinct sessions attributed to the plan: any session
	// that moved a step (task.transition on a plan-slug task) or authored the
	// plan (plan.captured / plan.presented / plan.approved).
	Sessions int
	// Unreported counts attributed sessions with no harvested tokens: a live
	// Claude Code session (tokens land only at SessionEnd), or one that ended
	// without a clean SessionEnd (the reaper never harvests). Real zero usage
	// cannot occur -- a session that ran at all processed input tokens -- so
	// total_tokens = 0 always means "not reported", never "used nothing".
	Unreported int
	// Shared counts attributed sessions that are also attributed to at least
	// one other plan. Their whole-session totals count toward each plan they
	// touched, so summing rollups across plans over-counts by exactly these;
	// surfaces must present per-plan numbers, never a cross-plan sum.
	Shared int
	// Tokens is SUM(sessions.total_tokens) over the reporting attributed
	// sessions: context volume including cache reads, not a billing figure, and
	// never to be mixed with the ~4 bytes/token injection estimates.
	Tokens int
}

// PlanTokenRollups computes the model-token rollup for every plan with at
// least one attributed session, keyed by (project, slug).
//
// A session is attributed through the event log, which is durable where the
// live linkage is not (tasks.claimed_by clears on release/close): every
// task.transition whose task carries a plan_slug, unioned with the plan
// lifecycle events whose payload names the slug -- so a captured plan carries
// its planning session's burn before any step is claimed. Sessions the log
// names but the sessions table no longer holds count as Unreported rather than
// disappearing: the attribution is real even when the tokens are gone.
func PlanTokenRollups(ctx context.Context, db *sql.DB) (map[PlanRef]PlanTokenRollup, error) {
	rows, err := db.QueryContext(ctx, `
		WITH plan_sessions AS (
			SELECT t.project_slug AS project, t.plan_slug AS slug, e.session_id AS session_id
			  FROM events e
			  JOIN tasks t ON t.id = e.item_id
			 WHERE e.kind = ? AND e.session_id <> '' AND t.plan_slug <> ''
			 UNION
			SELECT e.project_slug, json_extract(e.payload, '$.plan_slug'), e.session_id
			  FROM events e
			 WHERE e.kind IN (?, ?, ?) AND e.session_id <> ''
			   AND COALESCE(json_extract(e.payload, '$.plan_slug'), '') <> ''
		),
		spread AS (
			SELECT session_id, COUNT(*) AS plans FROM plan_sessions GROUP BY session_id
		)
		SELECT ps.project, ps.slug, COALESCE(s.total_tokens, 0), sp.plans
		  FROM plan_sessions ps
		  LEFT JOIN sessions s ON s.id = ps.session_id
		  JOIN spread sp ON sp.session_id = ps.session_id`,
		string(core.EventTaskTransition),
		string(core.EventPlanCaptured), string(core.EventPlanPresented), string(core.EventPlanApproved))
	if err != nil {
		return nil, fmt.Errorf("store.PlanTokenRollups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[PlanRef]PlanTokenRollup{}
	for rows.Next() {
		var ref PlanRef
		var tokens, plans int
		if err := rows.Scan(&ref.Project, &ref.Slug, &tokens, &plans); err != nil {
			return nil, fmt.Errorf("store.PlanTokenRollups: scan: %w", err)
		}
		r := out[ref]
		r.Sessions++
		if tokens > 0 {
			r.Tokens += tokens
		} else {
			r.Unreported++
		}
		if plans > 1 {
			r.Shared++
		}
		out[ref] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.PlanTokenRollups: %w", err)
	}
	return out, nil
}
