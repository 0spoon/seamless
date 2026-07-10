package mcp

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode"

	"github.com/mark3labs/mcp-go/mcp"
)

// jsonResult marshals v and returns it as a tool text result.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return mcp.NewToolResultError("internal error: marshal result"), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

// errResult returns a tool error result (not a Go error, per the SDK contract),
// prefixed with the tool name. Callers pass the owner's own local system errors,
// which carry no secrets.
func errResult(tool string, err error) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError(tool + ": " + err.Error()), nil
}

// argString reads and trims a string argument ("" when absent).
func argString(r mcp.CallToolRequest, key string) string {
	return strings.TrimSpace(r.GetString(key, ""))
}

// argRaw reads a string argument without trimming (for markdown bodies).
func argRaw(r mcp.CallToolRequest, key string) string {
	return r.GetString(key, "")
}

// argInt reads an integer argument, or def when absent/invalid.
func argInt(r mcp.CallToolRequest, key string, def int) int {
	return r.GetInt(key, def)
}

// argTags splits a comma-separated tags argument, trimming and dropping blanks.
func argTags(r mcp.CallToolRequest, key string) []string {
	return parseCommaList(argString(r, key))
}

// parseCommaList splits a comma-separated string, trimming and dropping blanks.
func parseCommaList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for t := range strings.SplitSeq(raw, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// appendUnique appends v to tags if not already present.
func appendUnique(tags []string, v string) []string {
	if slices.Contains(tags, v) {
		return tags
	}
	return append(tags, v)
}

// slugify converts a title into a filesystem-safe slug: lowercase, alphanumeric
// runs joined by single dashes, capped at 80 runes, "untitled" when empty.
func slugify(s string) string {
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
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "untitled"
	}
	if len([]rune(slug)) > 80 {
		slug = string([]rune(slug)[:80])
		slug = strings.Trim(slug, "-")
	}
	return slug
}
