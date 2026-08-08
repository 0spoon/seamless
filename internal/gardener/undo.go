package gardener

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/files"
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

// CanUndoResolution reports whether a resolved proposal can be taken back,
// whatever it was resolved into. Both rejection tiers always can -- neither a
// dismissal nor a hide has an effect to invert, and hiding forever would be a
// trap if the forever started before the owner could change their mind. Only an
// apply consults CanUndoApply, which stays the single source for that judgement
// and for the console's confirm policy.
func CanUndoResolution(status, kind string) bool {
	if status == store.ProposalApplied {
		return CanUndoApply(kind)
	}
	return true
}

// Undo returns a resolved proposal to the pending queue, inverting whatever its
// apply did. A rejection -- dismissed or hidden -- simply reopens, which is
// also how a hide-forever stops being forever. An apply first runs the per-kind
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
	case store.ProposalDismissed, store.ProposalHidden:
		// Both rejection tiers reopen the same way: neither had an effect, and
		// reopening clears whichever block the tier had put on the key.
		if err := store.ReopenProposal(ctx, s.db, id); err != nil {
			return err
		}
		s.record(ctx, id, map[string]any{"action": "undo_reject", "resolution": p.Status, "kind": p.Kind})
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
	case store.ProposalReproject, store.ProposalRelocate:
		// A relocate is a move like any other, so its inverse is the same move
		// back. It deliberately does NOT join consolidate/split in the
		// never-undoable set: the apply created nothing, it only changed one
		// memory's project, and an owner who tightened a fence must be able to
		// take back a repair they no longer want.
		return s.undoReproject(ctx, p, now)
	case store.ProposalRekind:
		return s.undoRekind(ctx, p, now)
	case store.ProposalAbandonPlan:
		return s.undoSettlePlan(ctx, p, plans.StatusAbandoned, now)
	case store.ProposalShipPlan:
		return s.undoSettlePlan(ctx, p, plans.StatusShipped, now)
	case store.ProposalMergePlans:
		return s.undoMergePlans(ctx, p, now)
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
	// Unarchive strips the tombstone and rewrites the whole file, and the
	// supersession guard is the read that decides whether it may -- both belong
	// inside the lock, or the undo restores a body someone else has moved on from.
	var restored core.Memory
	if err := s.mutateInactiveMemory(ctx, payloadString(p.Payload, "id"), func(ctx context.Context, mem core.Memory) error {
		if mem.SupersededBy != "" {
			return fmt.Errorf("memory %q has since been superseded -- restore it from that merge instead", mem.Name)
		}
		r, err := lifecycle.Unarchive(ctx, s.files, mem, now)
		if err != nil {
			return err
		}
		restored = r
		return nil
	}); err != nil {
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
	// Same shape as undoArchive: the "still superseded by this very merge" guard
	// is what authorizes the rewrite, so it reads the file under the lock rather
	// than a copy taken before it.
	var restored core.Memory
	if err := s.mutateInactiveMemory(ctx, dropID, func(ctx context.Context, drop core.Memory) error {
		if drop.SupersededBy != keepID {
			return fmt.Errorf("memory %q is no longer superseded by this merge's kept memory", drop.Name)
		}
		r, err := lifecycle.Unsupersede(ctx, s.files, drop, now)
		if err != nil {
			return err
		}
		restored = r
		return nil
	}); err != nil {
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
	idx, err := s.activeMemoryIndex(ctx, payloadString(p.Payload, "id"))
	if err != nil {
		return err
	}
	// Both sides of the move are locked, as in applyReproject: the memory's
	// current file and the one it is moving back into, since MoveMemory writes the
	// new path before dropping the old.
	var moved core.Memory
	if err := s.files.MutatePaths(ctx, []string{idx.FilePath, files.MemoryRelPath(from, idx.Name)}, func(ctx context.Context) error {
		mem, rerr := s.loadActiveMemory(ctx, payloadString(p.Payload, "id"))
		if rerr != nil {
			return rerr
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
		m, merr := s.files.MoveMemory(ctx, mem, from)
		if merr != nil {
			return merr
		}
		moved = m
		return nil
	}); err != nil {
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
	// The "is it still the kind this proposal set" guard decides the write, and
	// WriteMemory re-renders the whole file, so read and write are one locked step.
	var written core.Memory
	if err := s.mutateActiveMemory(ctx, payloadString(p.Payload, "id"), func(ctx context.Context, mem core.Memory) error {
		if mem.Kind != to {
			return fmt.Errorf("memory %q is now a %s, not the %s this proposal set", mem.Name, mem.Kind, to)
		}
		mem.Kind = from
		mem.Updated = now
		w, werr := s.files.WriteMemory(ctx, mem)
		if werr != nil {
			return werr
		}
		written = w
		return nil
	}); err != nil {
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
	// The capture hook rewrites this same note on every plan-file save, so the
	// retag reads and writes under the file's lock: the status guard is the read
	// that decides the write, and the write renders the whole file from it.
	if _, err := s.files.MutateNote(ctx, idx.FilePath, func(_ context.Context, note core.Note) (core.Note, error) {
		if current := plans.StatusFromTags(note.Tags); current != settled {
			return core.Note{}, fmt.Errorf("plan %q is now %s, not the %s this proposal set", note.Slug, current, settled)
		}
		basename := plans.Basename(note.Slug)
		note.Tags = plans.SetStatusTag(note.Tags, prior)
		note.Description = plans.NoteDescription(basename, plans.NoteIteration(note), prior)
		note.Updated = now
		return note, nil
	}); err != nil {
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

// inactiveMemoryIndex is activeMemoryIndex's mirror: it resolves a memory id to
// its INDEX row and requires the memory to be INACTIVE, which is the state an
// archive or merge left it in. A memory that is active again has already been
// restored by some other route.
func (s *Service) inactiveMemoryIndex(ctx context.Context, id string) (core.Memory, error) {
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
	return idx, nil
}

// loadInactiveMemory resolves an inactive memory id to its full on-disk content.
// As with loadActiveMemory, a caller that goes on to write must call it inside
// the file's lock -- mutateInactiveMemory is that path.
func (s *Service) loadInactiveMemory(ctx context.Context, id string) (core.Memory, error) {
	idx, err := s.inactiveMemoryIndex(ctx, id)
	if err != nil {
		return core.Memory{}, err
	}
	return s.files.Store().ReadMemory(idx.FilePath)
}

// mutateInactiveMemory is mutateActiveMemory for the undo direction: it runs fn
// with the file's lock held and the memory re-read (and re-checked for
// inactivity) inside it, so a memory restored by hand while the undo waited is
// refused rather than restored twice from a stale body.
func (s *Service) mutateInactiveMemory(ctx context.Context, id string, fn func(context.Context, core.Memory) error) error {
	idx, err := s.inactiveMemoryIndex(ctx, id)
	if err != nil {
		return err
	}
	return s.files.Mutate(ctx, idx.FilePath, func(ctx context.Context) error {
		mem, rerr := s.loadInactiveMemory(ctx, id)
		if rerr != nil {
			return rerr
		}
		return fn(ctx, mem)
	})
}
