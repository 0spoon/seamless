// Momentum: the latched milestone ledger.
//
// A short, honest set of once-ever milestones -- counts a project actually
// crossed and firsts that actually happened -- minted as milestone.reached
// events via events.RecordOnce, whose kind+item_id guarded insert is the
// once-ever latch. Every payload states its exact claim verbatim and the real
// count behind it ("100 memories written in seamless", count 100), never a
// score-shaped invention. The checks piggyback on the event recorder's write
// path, where the counts change: there is no polling pass, and while the
// momentum feature is off no threshold check runs and nothing is minted
// (the gamification-arcade discipline).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
)

// EventMilestoneReached is the milestone ledger's event kind: a latched
// milestone, minted at most once per item_id (the milestone key) via
// events.RecordOnce. Defined here rather than in internal/core because the
// milestone vocabulary -- keys, thresholds, claims -- lives in this file and
// the ledger's consumers read them from here.
const EventMilestoneReached core.EventKind = "milestone.reached"

// eventPlanShipped is the plan-settlement event kind the momentum L2 layer
// mints when a plan ships. That layer has not landed yet, so CheckMilestones
// carries its own completion detection (checkPlanShippedByTasks) -- but it
// already reacts to this kind too, and both paths mint through the same
// first-plan-shipped latch key, so whichever fires first wins and the other
// finds the latch set: no double mint in either order.
const eventPlanShipped core.EventKind = "plan.shipped"

// MilestoneMemoryWrittenThresholds are the per-project memory-written counts
// that mint a milestone, ascending. Exported maturity-style so the ledger's
// consumers and this computation agree by construction.
var MilestoneMemoryWrittenThresholds = []int{100, 500, 1000}

// MilestoneRecallAnsweredThreshold is the per-project count of answered
// recalls -- recall-tool calls that returned at least one hit -- that mints a
// milestone. Kind-browse listings are passive exposure, not answered recalls,
// and do not count (closed-loop-utility-signal-contract).
const MilestoneRecallAnsweredThreshold = 1000

// OnceRecorder is the once-ever latch the milestone layer mints through --
// events.Recorder.RecordOnce, named as a role interface so this package does
// not import internal/events (whose in-package tests import store).
type OnceRecorder interface {
	RecordOnce(ctx context.Context, e core.Event) (id string, recorded bool, err error)
}

// CheckMilestones runs the milestone checks the just-landed event e can have
// moved, minting any newly crossed milestone through rec. The event recorder
// calls it after every successful insert, so each COUNT already includes e.
//
// Gated on the effective momentum config, resolved live (base overlaid with
// the console's stored override, the same discipline as every momentum
// surface): off means no threshold check runs and nothing is minted. Events
// with no project slug move no per-project count and are skipped outright, as
// are all kinds outside the small watched set -- in particular
// milestone.reached itself, which bounds the recorder's re-entrant call.
func CheckMilestones(ctx context.Context, db *sql.DB, rec OnceRecorder, base config.Features, e core.Event) error {
	if e.ProjectSlug == "" {
		return nil
	}
	switch e.Kind {
	case core.EventSessionStarted, core.EventMemoryWritten, core.EventMemorySuperseded,
		core.EventInjected, core.EventTaskTransition, eventPlanShipped:
	default:
		return nil
	}
	eff, _, ferr := FeaturesConfig(ctx, db, base)
	if ferr != nil {
		eff = base // failure-soft per FeaturesConfig's contract; ferr still reaches the caller's log
	}
	if !eff.Momentum {
		return ferr
	}

	errs := ferr
	switch e.Kind {
	case core.EventMemoryWritten:
		errs = errors.Join(errs, checkMemoryWrittenMilestones(ctx, db, rec, e))
	case core.EventInjected:
		errs = errors.Join(errs, checkRecallAnsweredMilestone(ctx, db, rec, e))
	case core.EventMemorySuperseded:
		errs = errors.Join(errs, mintFirstSupersession(ctx, rec, e))
	case core.EventTaskTransition:
		errs = errors.Join(errs, checkPlanShippedByTasks(ctx, db, rec, e))
	case eventPlanShipped:
		errs = errors.Join(errs, mintFirstPlanShipped(ctx, rec, e.SessionID, e.ProjectSlug, payloadString(e.Payload, "slug", "plan"), 0))
	}
	// The birthday check piggybacks on every watched kind: a project's next
	// activity after an anniversary mints it, with no polling pass.
	errs = errors.Join(errs, checkProjectBirthday(ctx, db, rec, e))
	return errs
}

// checkMemoryWrittenMilestones counts the project's memory.written events
// (the triggering one included) and mints each threshold the count has
// reached. A threshold crossed while momentum was off latches on the next
// check after it comes on -- the claim stays exactly true ("100 memories
// written in demo") and the payload carries the real count at mint time.
func checkMemoryWrittenMilestones(ctx context.Context, db *sql.DB, rec OnceRecorder, e core.Event) error {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE kind = ? AND project_slug = ?`,
		string(core.EventMemoryWritten), e.ProjectSlug).Scan(&n); err != nil {
		return fmt.Errorf("store.CheckMilestones: count memory writes: %w", err)
	}
	var errs error
	for _, threshold := range MilestoneMemoryWrittenThresholds {
		if n < threshold {
			break // thresholds ascend
		}
		key := fmt.Sprintf("memories-%d:%s", threshold, e.ProjectSlug)
		_, _, err := rec.RecordOnce(ctx, core.Event{
			Kind: EventMilestoneReached, SessionID: e.SessionID,
			ProjectSlug: e.ProjectSlug, ItemID: key,
			Payload: map[string]any{
				"claim":     fmt.Sprintf("%d memories written in %s", threshold, e.ProjectSlug),
				"project":   e.ProjectSlug,
				"count":     n,
				"threshold": threshold,
			},
		})
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("store.CheckMilestones: mint %s: %w", key, err))
		}
	}
	return errs
}

// checkRecallAnsweredMilestone counts the project's answered recalls --
// retrieval.injected events with source "recall", the recall-tool hits the
// closed-loop contract classifies as query-gated demand -- and mints the
// threshold once reached. Kind-browse hits (source "recall-browse") and every
// briefing or prompt injection stay out of the count; the aggregation is
// read-only over the event log and leaves retrieval stats untouched.
func checkRecallAnsweredMilestone(ctx context.Context, db *sql.DB, rec OnceRecorder, e core.Event) error {
	if payloadString(e.Payload, "source") != "recall" {
		return nil
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events
		 WHERE kind = ? AND project_slug = ? AND json_extract(payload, '$.source') = 'recall'`,
		string(core.EventInjected), e.ProjectSlug).Scan(&n); err != nil {
		return fmt.Errorf("store.CheckMilestones: count answered recalls: %w", err)
	}
	if n < MilestoneRecallAnsweredThreshold {
		return nil
	}
	key := fmt.Sprintf("recalls-%d:%s", MilestoneRecallAnsweredThreshold, e.ProjectSlug)
	_, _, err := rec.RecordOnce(ctx, core.Event{
		Kind: EventMilestoneReached, SessionID: e.SessionID,
		ProjectSlug: e.ProjectSlug, ItemID: key,
		Payload: map[string]any{
			"claim":     fmt.Sprintf("%d recalls answered in %s", MilestoneRecallAnsweredThreshold, e.ProjectSlug),
			"project":   e.ProjectSlug,
			"count":     n,
			"threshold": MilestoneRecallAnsweredThreshold,
		},
	})
	if err != nil {
		return fmt.Errorf("store.CheckMilestones: mint %s: %w", key, err)
	}
	return nil
}

// mintFirstSupersession latches the project's first supersession: the first
// time its knowledge visibly turned over rather than just accumulated. The
// triggering memory.superseded event is the whole evidence, so no count query
// runs -- the RecordOnce latch is the entire check.
func mintFirstSupersession(ctx context.Context, rec OnceRecorder, e core.Event) error {
	key := "first-supersession:" + e.ProjectSlug
	_, _, err := rec.RecordOnce(ctx, core.Event{
		Kind: EventMilestoneReached, SessionID: e.SessionID,
		ProjectSlug: e.ProjectSlug, ItemID: key,
		Payload: map[string]any{
			"claim":   fmt.Sprintf("first supersession in %s", e.ProjectSlug),
			"project": e.ProjectSlug,
			"count":   1,
			"memory":  e.ItemID,
		},
	})
	if err != nil {
		return fmt.Errorf("store.CheckMilestones: mint %s: %w", key, err)
	}
	return nil
}

// checkPlanShippedByTasks is the layer's own plan-shipped detection, pending
// the L2 plan.shipped event: a plan ships when a step closing as done leaves
// every step in the plan closed (done or dropped -- the planRollups
// completeness test, "one step from shipped" reaching zero). Judged from the
// tasks table, never inferred: the transition's task row names the plan and
// project, and the grouped count is the claim's evidence. A plan whose final
// open step is dropped completes without shipping and mints nothing.
func checkPlanShippedByTasks(ctx context.Context, db *sql.DB, rec OnceRecorder, e core.Event) error {
	if payloadString(e.Payload, "to") != string(core.TaskDone) {
		return nil
	}
	var project, plan string
	err := db.QueryRowContext(ctx,
		`SELECT project_slug, plan_slug FROM tasks WHERE id = ?`, e.ItemID).Scan(&project, &plan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // the task is gone; nothing to judge
	}
	if err != nil {
		return fmt.Errorf("store.CheckMilestones: load task: %w", err)
	}
	if project == "" || plan == "" {
		return nil // not a plan step
	}
	var total, closed int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status IN (?, ?) THEN 1 ELSE 0 END), 0)
		 FROM tasks WHERE project_slug = ? AND plan_slug = ?`,
		string(core.TaskDone), string(core.TaskDropped), project, plan).Scan(&total, &closed); err != nil {
		return fmt.Errorf("store.CheckMilestones: plan rollup: %w", err)
	}
	if total == 0 || closed < total {
		return nil // steps remain open or in flight
	}
	return mintFirstPlanShipped(ctx, rec, e.SessionID, project, plan, total)
}

// mintFirstPlanShipped latches the project's first shipped plan. Both
// detection paths -- the task-completion judgment above and the L2
// plan.shipped event once it exists -- land here on the same key, so the
// kind+item_id latch guarantees a single mint regardless of which fires first.
func mintFirstPlanShipped(ctx context.Context, rec OnceRecorder, sessionID, project, plan string, steps int) error {
	claim := fmt.Sprintf("first plan shipped in %s", project)
	if plan != "" {
		claim = fmt.Sprintf("first plan shipped in %s: %s", project, plan)
	}
	payload := map[string]any{"claim": claim, "project": project, "count": 1}
	if plan != "" {
		payload["plan"] = plan
	}
	if steps > 0 {
		payload["steps"] = steps
	}
	key := "first-plan-shipped:" + project
	_, _, err := rec.RecordOnce(ctx, core.Event{
		Kind: EventMilestoneReached, SessionID: sessionID,
		ProjectSlug: project, ItemID: key, Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("store.CheckMilestones: mint %s: %w", key, err)
	}
	return nil
}

// checkProjectBirthday mints a registered project's yearly anniversary on its
// first watched activity at or past the date -- the item key carries the year
// ordinal, so RecordOnce latches each year exactly once, and a dormant year
// simply never mints. An unregistered slug has no registration date and earns
// no birthday (the maturity precedent). Age is judged against the triggering
// event's timestamp with AddDate, never day arithmetic.
func checkProjectBirthday(ctx context.Context, db *sql.DB, rec OnceRecorder, e core.Event) error {
	var createdStr string
	err := db.QueryRowContext(ctx,
		`SELECT created_at FROM projects WHERE slug = ?`, e.ProjectSlug).Scan(&createdStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store.CheckMilestones: load project: %w", err)
	}
	created, err := core.ParseTime(createdStr)
	if err != nil {
		return fmt.Errorf("store.CheckMilestones: parse project created_at: %w", err)
	}
	now := e.TS
	if now.IsZero() {
		now = time.Now().UTC()
	}
	years := 0
	for !created.AddDate(years+1, 0, 0).After(now) {
		years++
	}
	if years < 1 {
		return nil
	}
	claim := fmt.Sprintf("%d years of %s", years, e.ProjectSlug)
	if years == 1 {
		claim = "one year of " + e.ProjectSlug
	}
	key := fmt.Sprintf("birthday-%d:%s", years, e.ProjectSlug)
	_, _, err = rec.RecordOnce(ctx, core.Event{
		Kind: EventMilestoneReached, SessionID: e.SessionID,
		ProjectSlug: e.ProjectSlug, ItemID: key,
		Payload: map[string]any{
			"claim":      claim,
			"project":    e.ProjectSlug,
			"years":      years,
			"registered": core.FormatTime(created),
		},
	})
	if err != nil {
		return fmt.Errorf("store.CheckMilestones: mint %s: %w", key, err)
	}
	return nil
}

// payloadString returns the first of the named payload fields holding a
// non-empty string, or "".
func payloadString(p map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := p[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
