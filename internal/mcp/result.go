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

// argInt reads an integer argument, or def when absent/invalid.
func argInt(r mcp.CallToolRequest, key string, def int) int {
	return r.GetInt(key, def)
}

// argBool reads a boolean argument, or def when absent/invalid. The validator
// has already coerced a declared boolean property from a bool or its string
// forms ("true"/"false"), so this only ever sees a real bool.
func argBool(r mcp.CallToolRequest, key string, def bool) bool {
	return r.GetBool(key, def)
}

// argStrings reads a string-array argument, nil when absent.
//
// validateMiddleware is the only supported way in. It coerces every declared
// array property to []string -- from an array or the legacy comma-separated
// string alike -- before a handler runs, so that is the one shape this reads. A
// handler called with a hand-built request bypasses that guarantee; an
// unexpected shape then yields nil rather than panicking. Do not read that
// fallback as a second accepted form: coerceStrings owns the parsing, and a
// parser here would be one this package has to keep in step with that one.
func argStrings(r mcp.CallToolRequest, key string) []string {
	list, _ := r.GetArguments()[key].([]string)
	return list
}

// argObject reads an object argument, nil when absent. Same contract as
// argStrings: the validator has already coerced a declared object property --
// from an object or a JSON-object string -- to map[string]any.
func argObject(r mcp.CallToolRequest, key string) map[string]any {
	m, _ := r.GetArguments()[key].(map[string]any)
	return m
}

// argPresent reports whether a parameter was sent, under its canonical name
// only: validateMiddleware has already collapsed the aliases onto it and dropped
// the keys that mean absent (an empty array, and the empty string standing in
// for an absent enum/array/number/object).
//
// It is what an update tool asks to tell "leave this field alone" from "set this
// field to empty" -- the distinction GetString cannot make, since it answers ""
// to both. A present key is always non-nil and of the declared type: coercion
// rejects a null before any handler sees it.
func argPresent(r mcp.CallToolRequest, key string) bool {
	_, ok := r.GetArguments()[key]
	return ok
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
