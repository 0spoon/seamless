package gardener

// Stale-stage pass: a stage memory is pinned into every briefing while its
// Status header marks a live gate (open|in_progress|blocked). One whose header
// is done, missing, or unrecognized gates nothing; once it has also gone
// StaleStageDays without an update it is proposed for archiving. The staleness
// pass cannot retire these -- pinning re-injects a stage every session, so an
// abandoned stage never looks inactive to the activity metric -- which is why
// this pass keys off the update time instead, and why stages stay exempt from
// staleness archiving.

import (
	"context"
	"strconv"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// proposeStaleStages proposes archiving stage memories with no live gate and no
// update in StaleStageDays. 0 disables the pass. A stage named by a [[link]] in
// another memory's body is protected, same as staleness archiving.
func (s *Service) proposeStaleStages(ctx context.Context, seen map[string]struct{}) (int, error) {
	if s.cfg.StaleStageDays <= 0 {
		return 0, nil
	}
	cutoff := s.now().UTC().Add(-time.Duration(s.cfg.StaleStageDays) * 24 * time.Hour)
	mems, err := store.AllActiveMemories(ctx, s.db)
	if err != nil {
		return 0, err
	}
	referenced, complete, err := s.referencedNames(ctx)
	if err != nil {
		return 0, err
	}
	if !complete {
		// Same reasoning as the staleness pass: absence from the protection set
		// is only meaningful when every body was read.
		return 0, errProtectionIncomplete
	}

	created := 0
	for _, m := range mems {
		if m.Kind != core.KindStage || m.Updated.After(cutoff) {
			continue
		}
		if _, ref := referenced[m.Name]; ref {
			continue // still referenced by another memory
		}
		full, err := s.files.Store().ReadMemory(m.FilePath)
		if err != nil {
			// Fail-safe skip: an unreadable body means the gate is unknowable,
			// and skipping proposes nothing. (referencedNames read every body a
			// moment ago, so this is a race, not a systemic gap.)
			s.logger.Warn("gardener: read stage body; cannot judge its gate this pass", "name", m.Name, "error", err)
			continue
		}
		status, _ := core.ParseStageHeader(full.Body)
		if core.StageStatusLive(status) {
			continue // a live gate holds its pin at any age
		}
		key := "archive:" + m.ID
		if _, dup := seen[key]; dup {
			continue
		}
		gate := "status " + status
		if status == "" {
			gate = "no parsable Status header"
		}
		payload := map[string]any{
			"id": m.ID, "name": m.Name, "project": m.Project, "kind": string(m.Kind),
			"description":   m.Description,
			"reason":        "stage with " + gate + ", no update in " + strconv.Itoa(s.cfg.StaleStageDays) + "d",
			"last_activity": core.FormatTime(m.Updated),
		}
		if _, err := s.createProposal(ctx, store.ProposalArchive, key, payload, seen); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}
