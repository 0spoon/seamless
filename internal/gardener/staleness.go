package gardener

import (
	"context"
	"strconv"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// proposeArchives proposes archiving active memories that have seen no activity
// (update, injection, or read) for StalenessDays, subject to two protections:
//
//   - Kind: constraints and stages are never archived by staleness. A constraint
//     is a load-bearing rule and a stage carries gated status; neither becomes
//     irrelevant merely by going unread.
//   - References: a memory named by a [[link]] in another memory's body is kept,
//     since something still points at it.
func (s *Service) proposeArchives(ctx context.Context, seen map[string]struct{}) (int, error) {
	cutoff := s.now().UTC().Add(-time.Duration(s.cfg.StalenessDays) * 24 * time.Hour)
	stale, err := store.StaleMemories(ctx, s.db, cutoff)
	if err != nil {
		return 0, err
	}
	if len(stale) == 0 {
		return 0, nil
	}
	referenced, err := s.referencedNames(ctx)
	if err != nil {
		return 0, err
	}

	created := 0
	for _, m := range stale {
		if m.Kind == core.KindConstraint || m.Kind == core.KindStage {
			continue // protected kinds
		}
		if _, ref := referenced[m.Name]; ref {
			continue // still referenced by another memory
		}
		key := "archive:" + m.ID
		if _, dup := seen[key]; dup {
			continue
		}
		payload := map[string]any{
			"id": m.ID, "name": m.Name, "project": m.Project, "kind": string(m.Kind),
			"description": m.Description, "reason": "no activity in " + strconv.Itoa(s.cfg.StalenessDays) + "d",
			"last_activity": core.FormatTime(m.Updated),
		}
		if _, err := s.createProposal(ctx, store.ProposalArchive, key, payload, seen); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}
