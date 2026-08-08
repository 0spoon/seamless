package gardener

// Merge settlement: the third answer the stale-plan pass can give about a
// never-approved captured plan. Abandon says nothing came of it and ship says
// the work landed anyway; merge says the work landed under a DIFFERENT
// composition that already carries the steps, and folds the capture into it.
//
// This exists because two things mint plan slugs and, until the capture hook
// started naming the slug back to the agent, neither knew about the other:
// upsertPlanNote slugifies the plan file's H1, while an agent following the
// "plans as composition" recipe invents its own for tasks_add. Approval used to
// hide the split -- the tracking task lands on the captured slug -- so the
// strandings collect in the plans that were presented and never approved: a
// capture with a narrative and no steps beside a composition with steps and no
// narrative.
//
// Applying moves notes between compositions, which is more than the other two
// settlements do, so the bar to propose is deliberately high: the capture must
// own no steps (nothing of its own can be lost), the target must own some, and
// their titles must share enough significant words that the pass is reading a
// relationship rather than inventing one. Ties are declined, not guessed.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/store"
)

// mergeMinTokens is how many significant title words a stranded capture and a
// candidate composition must share before the pass calls them the same work.
// Three is strict on purpose: a wrong merge relocates a whole composition's
// notes, and leaving a duplicate standing is the cheaper mistake.
const mergeMinTokens = 3

// mergeCandidate is the composition a stranded capture should fold into.
type mergeCandidate struct {
	slug   string
	title  string
	shared int
}

// found reports whether a target was identified.
func (c mergeCandidate) found() bool { return c.slug != "" }

// reason renders the settlement rationale for the proposal payload.
func (c mergeCandidate) reason(days int) string {
	return fmt.Sprintf("unapproved for %dd and carries no steps of its own, while plan %q in the same"+
		" project holds the steps for what looks like the same work (%d shared title words)",
		days, c.slug, c.shared)
}

// mergeTarget looks for the composition a stranded capture belongs to. The step
// asymmetry is what makes the proposal safe: a capture with no steps owns
// nothing that a retag could lose, and a plan that DOES have steps is where the
// work actually accrued. Two candidates tied at the top is ambiguity, and the
// pass declines rather than picking one.
func (s *Service) mergeTarget(ctx context.Context, n core.Note, slug string) mergeCandidate {
	if slug == "" {
		return mergeCandidate{}
	}
	// A capture with steps of its own is a working plan, not a stranded one.
	own, err := store.ListTasksForPlan(ctx, s.db, n.Project, "", slug)
	if err != nil || len(own) > 0 {
		return mergeCandidate{}
	}
	tokens := map[string]struct{}{}
	addWordTokens(tokens, n.Title)
	if len(tokens) < mergeMinTokens {
		return mergeCandidate{} // too thin a title to match on at all
	}
	rollups, err := store.PlanRollupsForProject(ctx, s.db, n.Project)
	if err != nil {
		return mergeCandidate{}
	}

	var best mergeCandidate
	runnerUp := 0 // only the runner-up's SCORE matters, and only to detect a tie
	for _, r := range rollups {
		if r.Slug == slug || r.Total == 0 {
			continue
		}
		primary, _, ok, err := plans.Composition(ctx, s.db, r.Slug)
		if err != nil || !ok {
			continue // a task-only plan has no narrative to compare against
		}
		shared := tokenOverlap(tokens, primary.Title)
		if shared < mergeMinTokens {
			continue
		}
		if shared > best.shared {
			runnerUp = best.shared
			best = mergeCandidate{slug: r.Slug, title: primary.Title, shared: shared}
			continue
		}
		runnerUp = max(runnerUp, shared)
	}
	if best.shared == runnerUp {
		return mergeCandidate{} // tied candidates: no single answer to propose
	}
	return best
}

// applyMergePlans folds a stranded capture into the composition that carries
// the work: every note tagged plan:<source> is retagged plan:<target>, and the
// capture's own note takes the terminal merged status so it leaves the
// briefing's awaiting-approval lines. The capture's note moves FIRST and under
// its own lock, because it is also the status guard -- a plan approved since
// the proposal was raised is a real plan of its own, and refusing there means
// nothing has been moved yet.
func (s *Service) applyMergePlans(ctx context.Context, p store.Proposal, now time.Time) (map[string]any, error) {
	id := payloadString(p.Payload, "id")
	source := payloadString(p.Payload, "slug")
	target := payloadString(p.Payload, "merge_into")
	if source == "" || target == "" {
		return nil, fmt.Errorf("merge_plans proposal is missing a slug (source %q, target %q)", source, target)
	}
	if source == target {
		return nil, fmt.Errorf("merge_plans would fold plan %q into itself", source)
	}
	idx, ok, err := store.NoteByID(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("plan note %q no longer exists", id)
	}

	priorStatus := ""
	written, err := s.files.MutateNote(ctx, idx.FilePath, func(_ context.Context, note core.Note) (core.Note, error) {
		switch st := plans.StatusFromTags(note.Tags); st {
		case plans.StatusApproved:
			return core.Note{}, fmt.Errorf("plan %q was approved since this was proposed", note.Slug)
		case plans.StatusMerged:
			return core.Note{}, fmt.Errorf("plan %q was already merged", note.Slug)
		default:
			priorStatus = st
		}
		note.Tags = plans.SetStatusTag(retagPlan(note.Tags, target), plans.StatusMerged)
		note.Description = plans.NoteDescription(plans.Basename(note.Slug), plans.NoteIteration(note), plans.StatusMerged)
		note.Updated = now
		return note, nil
	})
	if err != nil {
		return nil, err
	}

	// The capture's supporting notes -- cached subagent runs and anything else
	// tagged into the composition -- follow it, so the target inherits the whole
	// narrative rather than a stranded plan note.
	moved, err := s.retagComposition(ctx, source, target, now)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"merged": written.Slug, "from": source, "into": target, "project": written.Project,
		"prior_status": priorStatus, "notes": moved,
	}, nil
}

// undoMergePlans moves the composition back: the notes recorded in the apply's
// result return to the source slug and the capture returns to the status it
// held. A note that has since been retagged elsewhere is left alone -- the undo
// restores what this apply did, never what happened afterwards.
func (s *Service) undoMergePlans(ctx context.Context, p store.Proposal, now time.Time) error {
	source := payloadString(p.Result, "from")
	target := payloadString(p.Result, "into")
	if source == "" || target == "" {
		return fmt.Errorf("merge_plans result records no slugs to undo")
	}
	for _, id := range payloadStrings(p.Result, "notes") {
		idx, ok, err := store.NoteByID(ctx, s.db, id)
		if err != nil {
			return err
		}
		if !ok {
			continue // deleted since; nothing to move back
		}
		if _, err := s.files.MutateNote(ctx, idx.FilePath, func(_ context.Context, note core.Note) (core.Note, error) {
			if plans.SlugFromTags(note.Tags) != target {
				return note, files.ErrNoChange
			}
			note.Tags = retagPlan(note.Tags, source)
			note.Updated = now
			return note, nil
		}); err != nil {
			return err
		}
	}

	id := payloadString(p.Payload, "id")
	idx, ok, err := store.NoteByID(ctx, s.db, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("plan note %q no longer exists", id)
	}
	prior := payloadString(p.Result, "prior_status")
	if prior == "" {
		prior = plans.StatusDraft
	}
	_, err = s.files.MutateNote(ctx, idx.FilePath, func(_ context.Context, note core.Note) (core.Note, error) {
		if plans.StatusFromTags(note.Tags) != plans.StatusMerged {
			return note, files.ErrNoChange // settled again since; leave that decision standing
		}
		note.Tags = plans.SetStatusTag(retagPlan(note.Tags, source), prior)
		note.Description = plans.NoteDescription(plans.Basename(note.Slug), plans.NoteIteration(note), prior)
		note.Updated = now
		return note, nil
	})
	return err
}

// retagComposition moves every note still tagged plan:<source> onto the target
// slug, returning the ids it moved so undo can invert exactly this set. Each
// note is retagged under its own lock: the capture hook rewrites these same
// files whenever a subagent finishes, and an unlocked read-then-write would
// erase a report that landed in between.
func (s *Service) retagComposition(ctx context.Context, source, target string, now time.Time) ([]string, error) {
	tagged, err := store.NotesByTag(ctx, s.db, "", plans.SlugTag(source))
	if err != nil {
		return nil, err
	}
	moved := make([]string, 0, len(tagged))
	for _, n := range tagged {
		mutated := false
		if _, err := s.files.MutateNote(ctx, n.FilePath, func(_ context.Context, note core.Note) (core.Note, error) {
			if plans.SlugFromTags(note.Tags) != source {
				return note, files.ErrNoChange
			}
			note.Tags = retagPlan(note.Tags, target)
			note.Updated = now
			mutated = true
			return note, nil
		}); err != nil {
			return moved, err
		}
		if mutated {
			moved = append(moved, n.ID)
		}
	}
	return moved, nil
}

// retagPlan replaces a note's plan:<slug> composition tag, preserving every
// other tag in order. A note carrying none gains one, so the caller never has
// to distinguish the two cases.
func retagPlan(tags []string, slug string) []string {
	out := make([]string, 0, len(tags)+1)
	replaced := false
	for _, t := range tags {
		if strings.HasPrefix(t, plans.SlugTagPrefix()) {
			if !replaced {
				out = append(out, plans.SlugTag(slug))
				replaced = true
			}
			continue
		}
		out = append(out, t)
	}
	if !replaced {
		out = append(out, plans.SlugTag(slug))
	}
	return out
}
