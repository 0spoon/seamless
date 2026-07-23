package retrieve

import "github.com/0spoon/seamless/internal/core"

// clipWords caps a briefing fragment at maxRunes runes, cutting at the last
// word boundary before the cap and appending an ellipsis, so a clipped line
// never ends mid-word. It is the display clip for the briefing's
// recent-findings (200) and sibling-findings (150) lines, named separately
// from sanitizeField's cap on purpose: the recall Hit JSON contract pins
// sanitizeField's output byte-for-byte (see Hit.Snippet/Similarity in
// recall.go), so the briefing's clip budgets must stay tunable without
// touching it. Delegates to core.TruncateWords -- one shared word-boundary
// implementation, the same one behind memory_write's description cap.
func clipWords(s string, maxRunes int) string {
	return core.TruncateWords(s, maxRunes)
}
