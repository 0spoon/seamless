package core

import "strings"

// Slugify turns arbitrary text into a filesystem- and URL-safe lowercase slug:
// lowercase, alphanumeric runs joined by single dashes, trimmed and capped at 80
// chars. Empty input yields "untitled". It is the canonical slugifier for
// derived project slugs and mirrors the importer's note/memory slugging.
func Slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	const maxLen = 80
	if len(out) > maxLen {
		out = strings.Trim(out[:maxLen], "-")
	}
	if out == "" {
		return "untitled"
	}
	return out
}
