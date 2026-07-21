package gardener

import (
	"context"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// Dead-weight thresholds: an active memory that briefings keep pushing while
// no query-gated signal ever pulls it. The exposure floor means it had its
// chance; the age floor protects new memories still earning history; the
// demand window matches the utility score's classes exactly.
const (
	deadWeightMinInjects = 20                  // session-start injections within the window
	deadWeightMinAgeDays = 14                  // memory must be at least this old
	deadWeightWindow     = 30 * 24 * time.Hour // exposure + demand horizon
)

// evaluateUtilityActivation latches utility ranking on for every project whose
// query-gated demand history satisfies the readiness thresholds (defined next
// to store.UtilityDemandByProject, shared with the console's progress view).
// Maintenance, not a proposal pass: it runs right after the retrieval-stats
// rebuild so the decision reads the same event state the scores were just
// built from. Once latched a project stays active (no flapping); the owner's
// per-project force and the global utility_mode knob are resolved at briefing
// time, not here. Best-effort -- a failure is logged and retried next pass.
func (s *Service) evaluateUtilityActivation(ctx context.Context) {
	now := s.now().UTC()
	activation, err := store.GetUtilityActivation(ctx, s.db)
	if err != nil {
		s.logger.Warn("gardener: utility activation state", "error", err)
		return
	}
	demand, err := store.UtilityDemandByProject(ctx, s.db, now, store.UtilityReadyWindow)
	if err != nil {
		s.logger.Warn("gardener: utility demand summary", "error", err)
		return
	}

	changed := false
	for project, d := range demand {
		st := activation.Projects[project]
		if st.ReadyAt != nil || !d.Ready(now) {
			continue
		}
		t := now
		st.ReadyAt = &t
		activation.Projects[project] = st
		changed = true
		s.logger.Info("gardener: utility ranking armed", "project", project,
			"demand_events", d.RecentEvents, "demand_memories", d.RecentMemories)
		s.record(ctx, "", map[string]any{
			"action": "utility_armed", "project": project,
			"demand_events": d.RecentEvents, "demand_memories": d.RecentMemories,
		})
	}
	if !changed {
		return
	}
	if err := store.SetUtilityActivation(ctx, s.db, activation); err != nil {
		s.logger.Warn("gardener: save utility activation", "error", err)
	}
}

// proposeDeadWeight proposes archiving active memories that briefings inject
// constantly but that never earn a recall hit, prompt match, or explicit read.
// It reuses the archive proposal kind and key namespace (the stale-stage
// precedent), so a memory the staleness pass already flagged is never proposed
// twice, and the owner's existing apply/dismiss flow just works. Kind and
// reference protections mirror proposeArchives: some memories legitimately
// steer silently, which is exactly why this only ever proposes.
func (s *Service) proposeDeadWeight(ctx context.Context, seen map[string]struct{}) (int, error) {
	now := s.now().UTC()
	since := now.Add(-deadWeightWindow)
	exposure, err := store.BriefingExposureSince(ctx, s.db, since)
	if err != nil {
		return 0, err
	}
	demand, err := store.DemandItemIDsSince(ctx, s.db, since)
	if err != nil {
		return 0, err
	}
	mems, err := store.AllActiveMemories(ctx, s.db)
	if err != nil {
		return 0, err
	}
	referenced, complete, err := s.referencedNames(ctx)
	if err != nil {
		return 0, err
	}
	if !complete {
		return 0, errProtectionIncomplete
	}

	ageCutoff := now.AddDate(0, 0, -deadWeightMinAgeDays)
	created := 0
	for _, m := range mems {
		if m.Kind == core.KindConstraint || m.Kind == core.KindStage {
			continue // protected kinds (pinned by design, injected every session)
		}
		if m.Favorite {
			continue // an explicit star outranks the implicit silence
		}
		if _, ref := referenced[m.Name]; ref {
			continue
		}
		if m.Created.After(ageCutoff) || exposure[m.ID] < deadWeightMinInjects {
			continue
		}
		if _, ok := demand[m.ID]; ok {
			continue
		}
		key := "archive:" + m.ID
		if _, dup := seen[key]; dup {
			continue
		}
		payload := map[string]any{
			"id": m.ID, "name": m.Name, "project": m.Project, "kind": string(m.Kind),
			"description": m.Description,
			"reason": fmt.Sprintf("dead weight: briefing-injected %dx in 30d, zero recall/prompt/read demand",
				exposure[m.ID]),
			"last_activity": core.FormatTime(m.Updated),
		}
		if _, err := s.createProposal(ctx, store.ProposalArchive, key, payload, seen); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}
