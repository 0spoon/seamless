package retrieve

import (
	"context"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// expandLinks scans the top fused memory hits for [[name]] links and adds each
// referenced, in-scope, active memory to acc as a "linked" candidate, returning
// the neighbor ids added (so the caller can hydrate them). It contributes a
// modest RRF-style score by discovery order, so a linked neighbor competes as if
// it were another source's low-ranked hit -- surfaced, but below genuine matches.
// It is a no-op without a body reader (index rows carry no body to scan).
func (s *Service) expandLinks(ctx context.Context, ordered []string, acc map[string]*fusedItem, mems map[string]core.Memory, project string) ([]string, error) {
	if s.bodyReader == nil {
		return nil, nil
	}
	var neighbors []string
	examined := 0
	for _, id := range ordered {
		if examined >= linkExpandFrom {
			break
		}
		f := acc[id]
		if f == nil || f.kind != "memory" {
			continue
		}
		m, ok := mems[id]
		if !ok || !m.Active() || !scopeVisible(m.Project, project) {
			continue
		}
		examined++

		full, err := s.bodyReader.ReadMemory(m.FilePath)
		if err != nil {
			s.logger.Warn("retrieve.expandLinks: read body", "id", id, "error", err)
			continue
		}
		for _, name := range core.WikiLinks(full.Body) {
			nb, ok, err := s.resolveLinkedMemory(ctx, project, name)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if _, exists := acc[nb.ID]; exists {
				continue // already a candidate (from semantic/fts or an earlier link)
			}
			rank := len(neighbors)
			acc[nb.ID] = &fusedItem{kind: "memory", linked: true, score: 1.0 / float64(rrfK+rank+1)}
			mems[nb.ID] = nb
			neighbors = append(neighbors, nb.ID)
		}
	}
	return neighbors, nil
}

// resolveLinkedMemory resolves a [[name]] reference to an active memory in the
// project scope, falling back to a global memory of the same name. found is false
// when nothing matches (a dangling link is simply ignored).
func (s *Service) resolveLinkedMemory(ctx context.Context, project, name string) (core.Memory, bool, error) {
	m, ok, err := store.MemoryByName(ctx, s.db, project, name)
	if err != nil || ok {
		return m, ok, err
	}
	if project != "" {
		return store.MemoryByName(ctx, s.db, "", name)
	}
	return core.Memory{}, false, nil
}
