package gardener

import (
	"context"

	"github.com/0spoon/seamless/internal/store"
)

// proposeMerges scans every pair of active memories embedded under the current
// model and proposes a merge for any pair whose cosine similarity clears the
// dedup threshold. The newer memory (by updated_at) is suggested as the one to
// keep; the older as the one to drop. It is a no-op without an embedder (no
// vectors to compare). O(n^2) is fine at this corpus scale (hundreds of items).
func (s *Service) proposeMerges(ctx context.Context, seen map[string]struct{}) (int, error) {
	if s.embedder == nil {
		return 0, nil
	}
	vecs, err := store.ActiveMemoryVectors(ctx, s.db, s.embedder.Model())
	if err != nil {
		return 0, err
	}
	created := 0
	for i := range vecs {
		for j := i + 1; j < len(vecs); j++ {
			a, b := vecs[i], vecs[j]
			if len(a.Vec) == 0 || len(a.Vec) != len(b.Vec) {
				continue // corrupt or cross-model dimensionality; not comparable
			}
			score := store.Cosine(a.Vec, b.Vec)
			if score < s.cfg.DedupThreshold {
				continue
			}
			key := mergeKey(a.ID, b.ID)
			if _, dup := seen[key]; dup {
				continue
			}
			keep, drop := a, b
			if drop.UpdatedAt.After(keep.UpdatedAt) {
				keep, drop = b, a // keep the more recently updated memory
			}
			payload := map[string]any{
				"score": round2(score),
				"keep":  memoryBrief(keep),
				"drop":  memoryBrief(drop),
			}
			if err := s.createProposal(ctx, store.ProposalMerge, key, payload, seen); err != nil {
				return created, err
			}
			created++
		}
	}
	return created, nil
}

// mergeKey is the stable dedup key for a merge proposal: the two memory ids in a
// canonical (sorted) order so the pair is recognized regardless of scan order.
func mergeKey(idA, idB string) string {
	if idB < idA {
		idA, idB = idB, idA
	}
	return "merge:" + idA + "|" + idB
}

// memoryBrief is the compact memory descriptor embedded in a proposal payload.
func memoryBrief(m store.MemoryVector) map[string]any {
	return map[string]any{
		"id": m.ID, "name": m.Name, "project": m.Project,
		"description": m.Description, "kind": m.Kind,
	}
}

// round2 rounds a similarity score to two decimals for a tidy payload.
func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
