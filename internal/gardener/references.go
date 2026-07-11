package gardener

import (
	"context"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// referencedNames scans every active memory's body for [[name]] links and
// returns the set of referenced memory names. A memory named in this set is
// protected from staleness archiving (something still points at it). Bodies are
// read best-effort: an unreadable file is skipped, not fatal.
func (s *Service) referencedNames(ctx context.Context) (map[string]struct{}, error) {
	mems, err := store.AllActiveMemories(ctx, s.db)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{})
	for _, m := range mems {
		full, err := s.files.Store().ReadMemory(m.FilePath)
		if err != nil {
			s.logger.Warn("gardener: read memory body for references", "name", m.Name, "error", err)
			continue
		}
		for _, name := range core.WikiLinks(full.Body) {
			set[name] = struct{}{}
		}
	}
	return set, nil
}
