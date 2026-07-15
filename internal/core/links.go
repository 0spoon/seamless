package core

import (
	"regexp"
	"strings"
)

// wikiLinkRe matches a wiki-style memory reference [[name]] or [[project/name]]
// (with an optional |alias or #anchor) in a memory or note body.
var wikiLinkRe = regexp.MustCompile(`\[\[([^\]\n]+)\]\]`)

// WikiLinks returns the referenced memory names in body's [[...]] links,
// normalized to the bare name (a "project/name" reference keeps the last
// segment; a trailing "|alias" or "#anchor" is dropped). Duplicates are removed,
// order of first appearance preserved. Returns nil when there are no links.
func WikiLinks(body string) []string {
	matches := wikiLinkRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	var out []string
	for _, m := range matches {
		name := WikiLinkName(m[1])
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// WikiLinkName normalizes a [[...]] inner reference to a bare memory name: a
// "project/name" reference keeps the last segment, and a trailing "|alias" or
// "#anchor" is dropped. It is the shared normalization for every wiki-link
// consumer: WikiLinks here, and the markdown renderer's goldmark extension
// (internal/markdown), which does its own [[...]] parsing but defers to this for
// the name.
func WikiLinkName(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.IndexAny(ref, "|#"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	return strings.TrimSpace(ref)
}
