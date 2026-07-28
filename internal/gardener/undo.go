package gardener

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/lifecycle"
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/store"
)

// undoActor is the actor recorded on task transitions an undo performs, so a
// dropped task reads as the gardener taking back its own suggestion.
const undoActor = "gardener"

// CanUndoApply reports whether an APPLIED proposal of this kind can be taken
// back. A dismissal is always undoable (it had no effect to invert); an apply is
// only undoable when its effect has a faithful inverse.
//
// The two exceptions are not oversights:
//   - consolidate folds sources into a target that may be a PRE-EXISTING memory
//     (applyConsolidate's resolve-or-create path), so "undo" cannot tell what it
//     created from what it merely wrote into.
//   - split creates projects that immediately start accruing content -- memories
//     move into them, sessions bind to them -- so retiring them again would
//     destroy work the apply did not create.
//
// Both keep an Apply confirm instead, which is the console's other guard.
func CanUndoApply(kind string) bool {
	switch kind {
	case store.ProposalConsolidate, store.ProposalSplit:
		return false
	default:
		return true
	}
}

// Undo returns a resolved proposal to the pending queue, inverting whatever its
// apply did. A dismissal simply reopens. An apply first runs the per-kind
// inverse, and every inverse is guarded by a precondition describing the world
// the apply left behind: if the world has moved on since (the memory was
// re-archived by hand, the task was completed, the plan was approved), the undo
// refuses with that reason and the proposal stays resolved rather than
// half-reverted.
func (s *Service) Undo(ctx context.Context, id string) error {
	p, ok, err := store.ProposalByID(ctx, s.db, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no proposal with id %q", id)
	}
	now := s.now().UTC()

	switch p.Status {
	case store.ProposalPending:
		return fmt.Errorf("proposal %q is still pending -- there is nothing to undo", id)
	case store.ProposalDismissed:
		if err := store.ReopenProposal(ctx, s.db, id); err != nil {
			return err
		}
		s.record(ctx, id, map[string]any{"action": "undo_dismiss", "kind": p.Kind})
		return nil
	}

	if !CanUndoApply(p.Kind) {
		return fmt.Errorf("an applied %s cannot be undone from here", p.Kind)
	}
	if err := s.invertApply(ctx, p, now); err != nil {
		return err // the proposal stays applied; nothing was half-reverted
	}
	if err := store.ReopenProposal(ctx, s.db, id); err != nil {
		return err
	}
	s.record(ctx, id, map[string]any{"action": "undo_apply", "kind": p.Kind})
	return nil
}

// invertApply runs the per-kind inverse of an applied proposal.
func (s *Service) invertApply(ctx context.Context, p store.Proposal, now time.Time) error {
	switch p.Kind {
	case store.ProposalArchive:
		return s.undoArchive(ctx, p, now)
	case store.ProposalMerge:
		return s.undoMerge(ctx, p, now)
	case store.ProposalDigest:
		return s.undoDigest(ctx, p)
	case store.ProposalReproject:
		return s.undoReproject(ctx, p, now)
	case store.ProposalRekind:
		return s.undoRekind(ctx, p, now)
	case store.ProposalAbandonPlan:
		return s.undoSettlePlan(ctx, p, plans.StatusAbandoned, now)
	case store.ProposalShipPlan:
		return s.undoSettlePlan(ctx, p, plans.StatusShipped, now)
	case store.ProposalMemoryWanted, store.ProposalToolError:
		return s.undoOpenedTask(ctx, p, now)
	default:
		return fmt.Errorf("no undo for proposal kind %q", p.Kind)
	}
}

// undoArchive brings an archived memory back into the active set. It refuses a
// memory that is active again (someone already restored it) or superseded (a
// later merge claimed it -- undoing the archive would drop that edge).
func (s *Service) undoArchive(ctx context.Context, p store.Proposal, now time.Time) error {
	mem, err := s.loadInactiveMemory(ctx, payloadString(p.Payload, "id"))
	if err != nil {
		return err
	}
	if mem.SupersededBy != "" {
		return fmt.Errorf("memory %q has since been superseded -- restore it from that merge instead", mem.Name)
	}
	restored, err := lifecycle.Unarchive(ctx, s.files, mem, now)
	if err != nil {
		return err
	}
	s.recordMemory(ctx, core.EventMemoryWritten, restored, map[string]any{
		"name": restored.Name, "restored": "archive", "by": "gardener",
	})
	return nil
}

// undoMerge un-supersedes the dropped memory, making both halves of the merge
// active again. It refuses unless the drop still points at the kept memory this
// very proposal named, so an unrelated later supersession is never unwound.
func (s *Service) undoMerge(ctx context.Context, p store.Proposal, now time.Time) error {
	keepID := payloadString(payloadMap(p.Payload, "keep"), "id")
	dropID := payloadString(payloadMap(p.Payload, "drop"), "id")
	if keepID == "" || dropID == "" {
		return errors.New("merge proposal missing keep/drop ids")
	}
	drop, err := s.loadInactiveMemory(ctx, dropID)
	if err != nil {
		return err
	}
	if drop.SupersededBy != keepID {
		return fmt.Errorf("memory %q is no longer superseded by this merge's kept memory", drop.Name)
	}
	restored, err := lifecycle.Unsupersede(ctx, s.files, drop, now)
	if err != nil {
		return err
	}
	s.recordMemory(ctx, core.EventMemoryWritten, restored, map[string]any{
		"name": restored.Name, "restored": "merge", "by": "gardener",
	})
	return nil
}

// undoDigest deletes the note the apply wrote. It refuses a note that is not
// the gardener's -- the created-by:gardener tag is what distinguishes the
// digest this proposal produced from a note the owner has since rewritten.
func (s *Service) undoDigest(ctx context.Context, p store.Proposal) error {
	noteID := payloadString(p.Result, "note_id")
	if noteID == "" {
		return errors.New("this digest was applied before results were recorded -- its note must be removed by hand")
	}
	idx, ok, err := store.NoteByID(ctx, s.db, noteID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the digest note has already been deleted")
	}
	if !slices.Contains(idx.Tags, "created-by:gardener") {
		return fmt.Errorf("note %q is no longer the gardener's digest -- delete it by hand if you meant to", idx.Title)
	}
	if err := s.files.Remove(ctx, idx.FilePath); err != nil {
		return err
	}
	if s.events != nil {
		s.record(ctx, noteID, map[string]any{"action": "undo_digest_note", "title": idx.Title})
	}
	return nil
}

// undoReproject moves the memory back to the project it came from, reusing
// applyReproject's name-clash guard in the opposite direction.
func (s *Service) undoReproject(ctx context.Context, p store.Proposal, now time.Time) error {
	if payloadBool(p.Result, "noop") {
		return nil // the apply moved nothing (already at the target)
	}
	from, to := payloadString(p.Result, "from"), payloadString(p.Result, "to")
	if to == "" {
		return errors.New("this move was applied before results were recorded -- move the memory back by hand")
	}
	mem, err := s.loadActiveMemory(ctx, payloadString(p.Payload, "id"))
	if err != nil {
		return err
	}
	if mem.Project != to {
		return fmt.Errorf("memory %q has since moved out of %s -- this undo would send it somewhere it never was", mem.Name, to)
	}
	if clash, found, cerr := store.MemoryByName(ctx, s.db, from, mem.Name); cerr != nil {
		return cerr
	} else if found && clash.ID != mem.ID {
		return fmt.Errorf("project %q already has an active memory named %q", from, mem.Name)
	}
	mem.Updated = now
	moved, err := s.files.MoveMemory(ctx, mem, from)
	if err != nil {
		return err
	}
	s.recordMemory(ctx, core.EventMemoryMoved, moved, map[string]any{
		"name": moved.Name, "from": to, "to": from, "restored": "reproject", "by": "gardener",
	})
	return nil
}

// undoRekind writes the memory's original kind back. It refuses a memory whose
// kind has changed again since, so a later reclassification is not silently
// reverted to a kind nobody chose.
func (s *Service) undoRekind(ctx context.Context, p store.Proposal, now time.Time) error {
	if payloadBool(p.Result, "noop") {
		return nil // the apply changed nothing (already at the target kind)
	}
	from := core.MemoryKind(payloadString(p.Payload, "from"))
	to := core.MemoryKind(payloadString(p.Payload, "to"))
	if !slices.Contains(core.MemoryKinds, from) {
		return fmt.Errorf("this reclassification recorded an unknown original kind %q", from)
	}
	mem, err := s.loadActiveMemory(ctx, payloadString(p.Payload, "id"))
	if err != nil {
		return err
	}
	if mem.Kind != to {
		return fmt.Errorf("memory %q is now a %s, not the %s this proposal set", mem.Name, mem.Kind, to)
	}
	mem.Kind = from
	mem.Updated = now
	written, err := s.files.WriteMemory(ctx, mem)
	if err != nil {
		return err
	}
	s.recordMemory(ctx, core.EventMemoryWritten, written, map[string]any{
		"name": written.Name, "kind_from": string(to), "kind_to": string(from),
		"restored": "rekind", "by": "gardener",
	})
	return nil
}

// undoSettlePlan retags a settled captured plan back to the status it held
// before (draft or presented). settled is the terminal status this proposal
// kind writes; a note no longer carrying it has been moved on by someone else.
func (s *Service) undoSettlePlan(ctx context.Context, p store.Proposal, settled string, now time.Time) error {
	prior := payloadString(p.Payload, "plan_status")
	if prior != plans.StatusDraft && prior != plans.StatusPresented {
		return fmt.Errorf("this settlement recorded no restorable prior status (%q)", prior)
	}
	idx, ok, err := store.NoteByID(ctx, s.db, payloadString(p.Payload, "id"))
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the plan note no longer exists")
	}
	note, err := s.files.Store().ReadNote(idx.FilePath)
	if err != nil {
		return err
	}
	if current := plans.StatusFromTags(note.Tags); current != settled {
		return fmt.Errorf("plan %q is now %s, not the %s this proposal set", note.Slug, current, settled)
	}
	basename := plans.Basename(note.Slug)
	note.Tags = plans.SetStatusTag(note.Tags, prior)
	note.Description = plans.NoteDescription(basename, plans.NoteIteration(note), prior)
	note.Updated = now
	if _, err := s.files.WriteNote(ctx, note); err != nil {
		return err
	}
	return nil
}

// undoOpenedTask drops the task the apply opened (memory_wanted / tool_error).
// A task the apply merely REUSED is left alone: it predates the apply, so
// dropping it would destroy something this proposal never created.
func (s *Service) undoOpenedTask(ctx context.Context, p store.Proposal, now time.Time) error {
	if payloadBool(p.Result, "reused") {
		return nil
	}
	taskID := payloadString(p.Result, "task_id")
	if taskID == "" {
		return errors.New("this proposal was applied before results were recorded -- close its task by hand")
	}
	t, err := store.TaskByID(ctx, s.db, taskID)
	if err != nil {
		return err
	}
	if t.CreatedBy != undoActor {
		return fmt.Errorf("task %q was not opened by the gardener", t.Title)
	}
	if t.Status != core.TaskOpen {
		return fmt.Errorf("task %q is already %s -- someone picked it up", t.Title, t.Status)
	}
	dropped := core.TaskDropped
	if _, err := store.UpdateTask(ctx, s.db, taskID, store.TaskPatch{Status: &dropped}, undoActor, now); err != nil {
		return err
	}
	if s.events != nil {
		if _, err := s.events.Record(ctx, core.Event{
			Kind: core.EventTaskTransition, ProjectSlug: t.ProjectSlug, ItemID: taskID,
			Payload: map[string]any{"to": string(core.TaskDropped), "restored": p.Kind, "by": "gardener"},
		}); err != nil {
			s.logger.Warn("gardener: record undo task event", "task", taskID, "error", err)
		}
	}
	return nil
}

// loadInactiveMemory is loadActiveMemory's mirror: it resolves a memory id to
// its full on-disk content and requires the memory to be INACTIVE, which is the
// state an archive or merge left it in. A memory that is active again has
// already been restored by some other route.
func (s *Service) loadInactiveMemory(ctx context.Context, id string) (core.Memory, error) {
	if id == "" {
		return core.Memory{}, errors.New("empty memory id")
	}
	idx, ok, err := store.MemoryByID(ctx, s.db, id)
	if err != nil {
		return core.Memory{}, err
	}
	if !ok {
		return core.Memory{}, fmt.Errorf("memory %q no longer exists", id)
	}
	if idx.InvalidAt == nil {
		return core.Memory{}, fmt.Errorf("memory %q is already active", idx.Name)
	}
	return s.files.Store().ReadMemory(idx.FilePath)
}
