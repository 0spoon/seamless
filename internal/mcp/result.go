package mcp

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// jsonResult marshals v and returns it as a tool text result. A marshal failure
// keeps the error's own text: it names the offending Go type, which is the only
// clue to a defect that is otherwise invisible from the agent's side. The tool
// name is not added -- unlike errResult, this is only reachable from a tool the
// caller just named in its own request. Like those errors, a marshal failure
// describes local types, never stored content.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return mcp.NewToolResultError("internal error: marshal result: " + err.Error()), nil
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

// bodyAliases are the interchangeable names for an item's markdown text. The
// create tools advertise "body"; the append tools historically used
// "content"/"text". Accepting all three means an agent primed on one tool's
// param name still succeeds on another -- the single most common field-name
// mistake in the field logs.
var bodyAliases = []string{"body", "content", "text"}

// argBody returns an item's markdown text, accepting body/content/text
// interchangeably. Whitespace is preserved (markdown); the first non-blank wins.
func argBody(r mcp.CallToolRequest) string {
	for _, k := range bodyAliases {
		if v := argRaw(r, k); strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// firstStringArg returns the first present string argument among keys (for
// update tools that read the raw args map to detect which fields changed), and
// whether any was present. Value is returned untrimmed.
func firstStringArg(args map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if v, ok := args[k].(string); ok {
			return v, true
		}
	}
	return "", false
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
