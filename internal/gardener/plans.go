package gardener

// Stale-plan pass: a captured Claude Code plan that was never approved
// (plan-status draft/presented) past StalePlanDays is proposed for
// abandonment. Applying retags the cc-plan note plan-status:abandoned, which
// removes it from the briefing's awaiting-approval lines; the note itself
// stays readable, as always.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/store"
)

// proposeStalePlans proposes abandoning captured plans still unapproved after
// StalePlanDays. 0 disables the pass.
func (s *Service) proposeStalePlans(ctx context.Context, seen map[string]struct{}) (int, error) {
	if s.cfg.StalePlanDays <= 0 {
		return 0, nil
	}
	cutoff := s.now().UTC().Add(-time.Duration(s.cfg.StalePlanDays) * 24 * time.Hour)
	notes, err := store.NotesByTag(ctx, s.db, "", plans.TagPlan)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, n := range notes {
		switch plans.StatusFromTags(n.Tags) {
		case plans.StatusDraft, plans.StatusPresented:
		default:
			continue // approved and abandoned plans are settled
		}
		if n.Updated.After(cutoff) {
			continue
		}
		key := "abandon_plan:" + n.ID
		if _, dup := seen[key]; dup {
			continue
		}
		payload := map[string]any{
			"id": n.ID, "slug": plans.SlugFromTags(n.Tags), "note_slug": n.Slug,
			"title": n.Title, "project": n.Project, "plan_status": plans.StatusFromTags(n.Tags),
			"reason":        "never approved in " + strconv.Itoa(s.cfg.StalePlanDays) + "d",
			"last_activity": core.FormatTime(n.Updated),
		}
		if _, err := s.createProposal(ctx, store.ProposalAbandonPlan, key, payload, seen); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// applyAbandonPlan retags the plan note plan-status:abandoned. A plan that was
// approved after the proposal was raised is left alone (error keeps the
// proposal pending, so the owner sees why and dismisses it).
func (s *Service) applyAbandonPlan(ctx context.Context, p store.Proposal, now time.Time) (map[string]any, error) {
	id := payloadString(p.Payload, "id")
	idx, ok, err := store.NoteByID(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("plan note %q no longer exists", id)
	}
	note, err := s.files.Store().ReadNote(idx.FilePath)
	if err != nil {
		return nil, err
	}
	if plans.StatusFromTags(note.Tags) == plans.StatusApproved {
		return nil, fmt.Errorf("plan %q was approved since this was proposed", note.Slug)
	}
	basename := plans.Basename(note.Slug)
	note.Tags = plans.SetStatusTag(note.Tags, plans.StatusAbandoned)
	note.Description = plans.NoteDescription(basename, plans.NoteIteration(note), plans.StatusAbandoned)
	note.Updated = now
	written, err := s.files.WriteNote(ctx, note)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"abandoned": written.Slug, "plan": plans.SlugFromTags(written.Tags), "project": written.Project,
	}, nil
}
