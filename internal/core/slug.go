package core

import (
	"strings"
	"unicode"
)

// SlugMaxRunes is the cap Slugify applies to its output.
const SlugMaxRunes = 80

// Slugify turns arbitrary text into a filesystem- and URL-safe lowercase slug:
// lowercase, letter/number runs joined by single dashes, trimmed and capped at
// SlugMaxRunes runes. Empty input yields "untitled".
//
// It is the ONE slugifier: every derived slug (project slugs from a repo dir or
// project_create, note slugs from notes_create/capture_url, plan slugs from a
// captured plan title, imported memory names) flows through it, so the same
// title yields the same slug whatever the entry path.
//
// Letters and numbers are kept per Unicode, not ASCII, so a non-Latin title
// slugs to itself rather than collapsing to "untitled" -- which matters because
// note slugs become filenames and notes_create does not disambiguate a
// collision. The cap is in runes, so it can never split a multi-byte rune.
func Slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if rs := []rune(out); len(rs) > SlugMaxRunes {
		out = strings.Trim(string(rs[:SlugMaxRunes]), "-")
	}
	if out == "" {
		return "untitled"
	}
	return out
}
