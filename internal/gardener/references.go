package gardener

import (
	"context"
	"regexp"
	"strings"

	"github.com/0spoon/seamless/internal/store"
)

// linkRe matches a wiki-style memory reference [[name]] or [[project/name]] in a
// memory body. The capture group is the inner reference text.
var linkRe = regexp.MustCompile(`\[\[([^\]\n]+)\]\]`)

// parseLinks returns the referenced names inside [[...]] links in body,
// normalized to the bare memory name (a "project/name" reference keeps only the
// last path segment). Empty results for a body with no links.
func parseLinks(body string) []string {
	matches := linkRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if name := linkName(m[1]); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// linkName normalizes a [[...]] inner reference to a bare memory name: it trims
// whitespace, drops a trailing "|alias" (Obsidian display text) and any "#anchor",
// and keeps only the last "/"-separated segment.
func linkName(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.IndexAny(ref, "|#"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	return strings.TrimSpace(ref)
}

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
		for _, name := range parseLinks(full.Body) {
			set[name] = struct{}{}
		}
	}
	return set, nil
}
