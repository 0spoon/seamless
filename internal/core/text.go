package core

import (
	"unicode"
	"unicode/utf8"
)

// Ellipsis is the single-rune horizontal ellipsis appended to truncated text.
const Ellipsis = "…" // …

// TruncateWords caps s at maxRunes runes, cutting on a word boundary and
// appending an ellipsis so the result never ends mid-word. The returned string,
// ellipsis included, is at most maxRunes runes. When s already fits it is
// returned unchanged. A single leading token longer than the budget has no
// boundary to honor, so it falls back to a hard rune cut. maxRunes <= 1 disables
// truncation (there is no room for content plus an ellipsis).
func TruncateWords(s string, maxRunes int) string {
	if maxRunes <= 1 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	r := []rune(s)
	budget := maxRunes - 1 // reserve one rune for the ellipsis
	// If the first dropped rune is not whitespace we are mid-word; back off to
	// the end of the previous word.
	end := budget
	if !unicode.IsSpace(r[end]) {
		for end > 0 && !unicode.IsSpace(r[end-1]) {
			end--
		}
	}
	// Drop trailing whitespace and dangling separator punctuation so the ellipsis
	// attaches cleanly to the last whole word.
	for end > 0 && (unicode.IsSpace(r[end-1]) || isTrailingPunct(r[end-1])) {
		end--
	}
	if end == 0 {
		// A single token longer than the budget: hard-cut it rather than emit a
		// bare ellipsis.
		end = budget
	}
	return string(r[:end]) + Ellipsis
}

// isTrailingPunct reports whether r is separator punctuation that should be
// trimmed off the tail of a truncated string before the ellipsis is appended.
func isTrailingPunct(r rune) bool {
	switch r {
	case ',', ';', ':', '-', '.', '/':
		return true
	}
	return false
}
