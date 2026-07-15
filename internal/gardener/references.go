package gardener

import (
	"context"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// referencedNames scans every active memory's body for [[name]] links and
// returns the set of referenced memory names. A memory named in this set is
// protected from staleness archiving (something still points at it).
//
// complete reports whether every active body was read. An unreadable body is
// skipped rather than fatal, but its links are precisely what protect OTHER
// memories, so a single skip makes the whole set untrustworthy: any name absent
// from it may in fact be protected by the body that could not be read. Callers
// that read absence as "unprotected" must not do so when complete is false.
func (s *Service) referencedNames(ctx context.Context) (map[string]struct{}, bool, error) {
	mems, err := store.AllActiveMemories(ctx, s.db)
	if err != nil {
		return nil, false, err
	}
	set := make(map[string]struct{})
	complete := true
	for _, m := range mems {
		full, err := s.files.Store().ReadMemory(m.FilePath)
		if err != nil {
			s.logger.Warn("gardener: read memory body for references; its [[links]] cannot protect anything this pass",
				"name", m.Name, "error", err)
			complete = false
			continue
		}
		for _, name := range core.WikiLinks(full.Body) {
			set[name] = struct{}{}
		}
	}
	return set, complete, nil
}
