package gardener

import (
	"context"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/lifecycle"
	"github.com/0spoon/seamless/internal/store"
)

// Apply carries out a pending proposal and marks it applied. The effect depends
// on the kind: an archive retires the memory (invalid, but still readable), a
// merge supersedes the "drop" memory by the "keep" memory, and a digest writes
// the summary as a note. If the effect cannot be carried out (e.g. a referenced
// memory has since been deleted), the proposal is left pending and an error is
// returned, so the owner can retry or dismiss.
func (s *Service) Apply(ctx context.Context, id string) (map[string]any, error) {
	p, ok, err := store.ProposalByID(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no proposal with id %q", id)
	}
	if p.Status != store.ProposalPending {
		return nil, fmt.Errorf("proposal %q is already %s", id, p.Status)
	}
	now := s.now().UTC()

	var result map[string]any
	switch p.Kind {
	case store.ProposalArchive:
		result, err = s.applyArchive(ctx, p, now)
	case store.ProposalMerge:
		result, err = s.applyMerge(ctx, p, now)
	case store.ProposalDigest:
		result, err = s.applyDigest(ctx, p, now)
	case store.ProposalConsolidate:
		result, err = s.applyConsolidate(ctx, p, now)
	default:
		return nil, fmt.Errorf("unknown proposal kind %q", p.Kind)
	}
	if err != nil {
		return nil, err // leave the proposal pending; the effect did not happen
	}
	if err := store.ResolveProposal(ctx, s.db, id, store.ProposalApplied, now); err != nil {
		return nil, err
	}
	s.record(ctx, id, map[string]any{"action": "apply", "kind": p.Kind})
	result["status"] = "applied"
	result["kind"] = p.Kind
	return result, nil
}

// Dismiss marks a pending proposal dismissed without any side effect. Its key
// stays known, so the gardener will not raise the same suggestion again.
func (s *Service) Dismiss(ctx context.Context, id string) error {
	if err := store.ResolveProposal(ctx, s.db, id, store.ProposalDismissed, s.now().UTC()); err != nil {
		return err
	}
	s.record(ctx, id, map[string]any{"action": "dismiss"})
	return nil
}

func (s *Service) applyArchive(ctx context.Context, p store.Proposal, now time.Time) (map[string]any, error) {
	mem, err := s.loadActiveMemory(ctx, payloadString(p.Payload, "id"))
	if err != nil {
		return nil, err
	}
	updated, err := lifecycle.Archive(ctx, s.files, mem, "gardener staleness", now)
	if err != nil {
		return nil, err
	}
	s.recordMemory(ctx, core.EventMemoryArchived, updated, map[string]any{"name": updated.Name, "by": "gardener"})
	return map[string]any{"archived": lifecycle.MemoryRef(updated.Project, updated.Name)}, nil
}

func (s *Service) applyMerge(ctx context.Context, p store.Proposal, now time.Time) (map[string]any, error) {
	keepID := payloadString(payloadMap(p.Payload, "keep"), "id")
	dropID := payloadString(payloadMap(p.Payload, "drop"), "id")
	if keepID == "" || dropID == "" {
		return nil, fmt.Errorf("merge proposal missing keep/drop ids")
	}
	if keepID == dropID {
		return nil, fmt.Errorf("merge proposal keep and drop are the same memory")
	}
	keep, err := s.loadActiveMemory(ctx, keepID)
	if err != nil {
		return nil, fmt.Errorf("keep memory: %w", err)
	}
	drop, err := s.loadActiveMemory(ctx, dropID)
	if err != nil {
		return nil, fmt.Errorf("drop memory: %w", err)
	}
	updated, err := lifecycle.Supersede(ctx, s.files, drop, keep, now)
	if err != nil {
		return nil, err
	}
	s.recordMemory(ctx, core.EventMemorySuperseded, updated, map[string]any{
		"name": updated.Name, "superseded_by": keep.ID, "by": "gardener",
	})
	return map[string]any{
		"kept":    lifecycle.MemoryRef(keep.Project, keep.Name),
		"dropped": lifecycle.MemoryRef(updated.Project, updated.Name),
	}, nil
}

func (s *Service) applyDigest(ctx context.Context, p store.Proposal, now time.Time) (map[string]any, error) {
	title := payloadString(p.Payload, "title")
	body := payloadString(p.Payload, "body")
	if title == "" || body == "" {
		return nil, fmt.Errorf("digest proposal missing title/body")
	}
	id, err := core.NewID()
	if err != nil {
		return nil, err
	}
	project := payloadString(p.Payload, "project")
	note := core.Note{
		ID: id, Title: title, Slug: core.Slugify(title), Description: "Monthly session digest",
		Project: project, Body: body, Tags: []string{"created-by:gardener", "digest"},
		Created: now, Updated: now,
	}
	written, err := s.files.WriteNote(ctx, note)
	if err != nil {
		return nil, err
	}
	if s.events != nil {
		s.record(ctx, written.ID, map[string]any{"action": "digest_note", "title": title})
	}
	return map[string]any{"note_id": written.ID, "title": written.Title}, nil
}

// applyConsolidate writes a new unified memory (or reuses an existing one with
// the same project+name) and supersedes every named source by it. It is
// idempotent: a retry after a partial apply reuses the already-written target
// and skips sources that were already superseded, so it converges rather than
// duplicating -- important because Apply leaves the proposal pending on failure.
func (s *Service) applyConsolidate(ctx context.Context, p store.Proposal, now time.Time) (map[string]any, error) {
	name := payloadString(p.Payload, "name")
	project := payloadString(p.Payload, "project")
	body := payloadString(p.Payload, "body")
	if name == "" || body == "" {
		return nil, fmt.Errorf("consolidate proposal missing name/body")
	}

	// Resolve-or-create the unified target. An already-active (project,name)
	// memory is reused, so a retry does not write a second copy; on a first run
	// this also means a name matching an existing memory folds the sources into it.
	target, ok, err := store.MemoryByName(ctx, s.db, project, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		id, nerr := core.NewID()
		if nerr != nil {
			return nil, nerr
		}
		written, werr := s.files.WriteMemory(ctx, core.Memory{
			ID: id, Kind: core.MemoryKind(payloadString(p.Payload, "kind")),
			Name: name, Description: payloadString(p.Payload, "description"),
			Project: project, Body: body,
			Created: now, Updated: now, ValidFrom: now,
		})
		if werr != nil {
			return nil, werr
		}
		target = written
		s.recordMemory(ctx, core.EventMemoryWritten, target, map[string]any{"name": target.Name, "by": "gardener"})
	}

	// Supersede each still-active source by the target. Sources already inactive
	// (superseded on a prior partial apply) and the target itself are skipped.
	superseded := make([]string, 0)
	for _, src := range payloadList(p.Payload, "sources") {
		srcID := payloadString(src, "id")
		if srcID == "" || srcID == target.ID {
			continue
		}
		idx, found, ierr := store.MemoryByID(ctx, s.db, srcID)
		if ierr != nil {
			return nil, ierr
		}
		if !found || idx.InvalidAt != nil {
			continue // already gone or already superseded
		}
		full, rerr := s.files.Store().ReadMemory(idx.FilePath)
		if rerr != nil {
			return nil, rerr
		}
		updated, serr := lifecycle.Supersede(ctx, s.files, full, target, now)
		if serr != nil {
			return nil, serr
		}
		s.recordMemory(ctx, core.EventMemorySuperseded, updated, map[string]any{
			"name": updated.Name, "superseded_by": target.ID, "by": "gardener",
		})
		superseded = append(superseded, lifecycle.MemoryRef(updated.Project, updated.Name))
	}

	return map[string]any{
		"created":    lifecycle.MemoryRef(target.Project, target.Name),
		"superseded": superseded,
	}, nil
}

// loadActiveMemory resolves a memory id to its full on-disk content, erroring if
// the memory no longer exists or is already inactive (archived/superseded) -- in
// either case the proposal's effect no longer applies.
func (s *Service) loadActiveMemory(ctx context.Context, id string) (core.Memory, error) {
	if id == "" {
		return core.Memory{}, fmt.Errorf("empty memory id")
	}
	idx, ok, err := store.MemoryByID(ctx, s.db, id)
	if err != nil {
		return core.Memory{}, err
	}
	if !ok {
		return core.Memory{}, fmt.Errorf("memory %q no longer exists", id)
	}
	if idx.InvalidAt != nil {
		return core.Memory{}, fmt.Errorf("memory %q is already inactive", id)
	}
	return s.files.Store().ReadMemory(idx.FilePath)
}

// recordMemory appends a memory lifecycle event best-effort.
func (s *Service) recordMemory(ctx context.Context, kind core.EventKind, m core.Memory, payload map[string]any) {
	if s.events == nil {
		return
	}
	if _, err := s.events.Record(ctx, core.Event{
		Kind: kind, ProjectSlug: m.Project, ItemID: m.ID, Payload: payload,
	}); err != nil {
		s.logger.Warn("gardener: record memory event", "kind", kind, "error", err)
	}
}

// payloadString reads a string field from a proposal payload map ("" if absent
// or not a string).
func payloadString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// payloadMap reads a nested object field from a proposal payload (nil if absent).
func payloadMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

// payloadList reads an array-of-objects field from a proposal payload (nil if
// absent), e.g. a consolidate proposal's "sources".
func payloadList(m map[string]any, key string) []map[string]any {
	if m == nil {
		return nil
	}
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if obj, ok := v.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}
