package mcp

import (
	"strings"

	"github.com/0spoon/seamless/internal/core"
)

// mishapMemoryIDs returns the ids of memories whose name appears verbatim in a
// mishap's text. It is the ingestion half of mishap-memory linkage: the scan
// runs exactly once, when session_end records the report, and the matches
// persist in the agent.mishap event payload (item_ids) -- the event log is
// durable, so nothing ever re-matches later against a corpus that has moved on.
//
// A name matches only as a whole slug token: the occurrence must be bounded on
// both sides by the text's edge or a non-slug byte, so a name that is a prefix
// of a longer name (chroma-boot vs chroma-boot-race) never false-matches inside
// it. Matching is case-sensitive -- the tool description asks agents to name
// the violated memory by its exact slug -- and the caller controls scope by
// passing only the memories the report may name (the session project's own plus
// global; never another project's).
func mishapMemoryIDs(text string, memories []core.Memory) []string {
	var ids []string
	for _, m := range memories {
		if m.Name == "" || m.ID == "" {
			continue
		}
		if containsSlugToken(text, m.Name) {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// containsSlugToken reports whether slug occurs in text delimited by non-slug
// bytes (or the text's edges). Slug bytes are ASCII letters, digits, '-' and
// '_' -- the alphabet memory names are written in -- so every byte of a
// multi-byte rune is a delimiter and the byte-level boundary check is UTF-8
// safe.
func containsSlugToken(text, slug string) bool {
	for from := 0; ; {
		i := strings.Index(text[from:], slug)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(slug)
		if (start == 0 || !isSlugByte(text[start-1])) && (end == len(text) || !isSlugByte(text[end])) {
			return true
		}
		from = start + 1
	}
}

// isSlugByte reports whether b belongs to the slug alphabet.
func isSlugByte(b byte) bool {
	switch {
	case 'a' <= b && b <= 'z', 'A' <= b && b <= 'Z', '0' <= b && b <= '9':
		return true
	case b == '-', b == '_':
		return true
	}
	return false
}
