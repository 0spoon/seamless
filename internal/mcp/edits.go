package mcp

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// The shared search/replace engine behind memory_edit and notes_edit.
//
// The contract is Claude Code's Edit tool, deliberately: exact string match,
// unique-or-fail unless replace_all, all-or-nothing application. Models are
// trained on exactly this shape, and every failure mode is verifiable from the
// arguments alone -- unlike a line range (numbers drift silently), a regex
// (unbounded blast radius), or a fuzzy patch apply (a mismatched hunk lands
// somewhere plausible instead of erroring). Do not soften any of the guards:
// each one turns a silent corruption into a message the agent can act on.

// maxEditDiffRunes bounds the unified diff echoed back to the caller. The diff
// is a convenience -- the authority is the new content_hash -- and a whole-body
// rewrite expressed as one edit would otherwise return the entire document twice
// over, on a transport with a 1MB cap.
const maxEditDiffRunes = 8000

// editsParamDescription is the shared prose for the edits[] parameter, so the
// two tools advertise one contract rather than two paraphrases of it.
const editsParamDescription = "ordered list of exact search/replace edits, applied in order to the CURRENT body. " +
	"Each is {old_string, new_string, replace_all?}. old_string must match the body EXACTLY (whitespace and " +
	"indentation included) and must be unique unless replace_all is true; include surrounding lines to make it " +
	"unique. All-or-nothing: if any edit fails to match, nothing is written."

// editsItemSchema is the JSON Schema for one element of edits[]. It is shared so
// the two tools cannot drift, and so docsgen renders one description of the shape.
func editsItemSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"old_string": map[string]any{
				"type":        "string",
				"description": "exact text to find in the current body (verbatim, including whitespace)",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "replacement text; must differ from old_string. Empty string deletes the match.",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "replace every occurrence instead of requiring exactly one (default false)",
			},
		},
		"required": []string{"old_string", "new_string"},
	}
}

// withEditsParam declares the shared edits[] parameter on a tool. required marks
// it in the schema, which validateMiddleware enforces before any handler runs.
//
// The two tools differ, and the difference is not cosmetic. notes_edit does
// nothing else, so an absent edits[] there is a usage error worth catching at
// the boundary. memory_edit also carries description/tags_add/tags_remove, and a
// metadata-only edit is a legitimate call the memory surface has no OTHER way to
// express -- memory_write requires a body, so before this tool there was no way
// to fix a memory's description or clear a tag without resending its whole body.
// Requiring edits[] there would have left that gap open while the tool
// description claimed it was closed. memory_edit's handler carries its own
// "nothing to change" guard for the call that sends neither.
func withEditsParam(required bool) mcp.ToolOption {
	opts := []mcp.PropertyOption{
		mcp.Items(editsItemSchema()),
		mcp.Description(editsParamDescription),
	}
	if required {
		opts = append(opts, mcp.Required())
	}
	return mcp.WithArray("edits", opts...)
}

// edit is one search/replace operation.
type edit struct {
	Old        string
	New        string
	ReplaceAll bool
}

// parseEdits reads the edits[] argument into the typed form, rejecting the
// shapes that cannot mean anything: a missing or non-string field, an empty
// old_string (which matches everywhere), and old == new (a no-op the caller
// almost certainly did not intend -- Claude Code's Edit refuses it for the same
// reason).
//
// It does NOT trim: leading and trailing whitespace is part of an exact match,
// and trimming here would make an edit that the caller verified against the body
// silently miss.
func parseEdits(req mcp.CallToolRequest) ([]edit, error) {
	raw := argObjects(req, "edits")
	if len(raw) == 0 {
		return nil, errors.New("edits must contain at least one {old_string, new_string}")
	}
	out := make([]edit, 0, len(raw))
	for i, m := range raw {
		oldStr, err := editString(m, "old_string", i)
		if err != nil {
			return nil, err
		}
		newStr, err := editString(m, "new_string", i)
		if err != nil {
			return nil, err
		}
		if oldStr == "" {
			return nil, fmt.Errorf("edits[%d]: old_string must not be empty (an empty match has no location)", i)
		}
		if oldStr == newStr {
			return nil, fmt.Errorf("edits[%d]: old_string and new_string are identical, so the edit would change nothing", i)
		}
		replaceAll, err := editBool(m, "replace_all", i)
		if err != nil {
			return nil, err
		}
		out = append(out, edit{Old: oldStr, New: newStr, ReplaceAll: replaceAll})
	}
	return out, nil
}

// editString reads one required string field of an edit element.
func editString(m map[string]any, key string, i int) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", fmt.Errorf("edits[%d]: %s is required", i, key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("edits[%d].%s: expected a string, got %s", i, key, jsonTypeName(v))
	}
	return s, nil
}

// editBool reads the optional replace_all flag, accepting the string forms the
// validator accepts elsewhere (it cannot reach inside an array element to
// coerce them itself).
func editBool(m map[string]any, key string, i int) (bool, error) {
	v, ok := m[key]
	if !ok {
		return false, nil
	}
	b, err := coerceBool(v, fmt.Sprintf("edits[%d].%s", i, key))
	if err != nil {
		return false, err
	}
	return b, nil
}

// applyEdits applies every edit to body in order and returns the result.
//
// All-or-nothing: the first edit that does not match exactly once (or at all,
// with replace_all) aborts with an error naming the count, and the caller writes
// nothing. Later edits see the output of earlier ones, which is what lets an
// agent express two changes to overlapping regions as an ordered pair.
func applyEdits(body string, edits []edit) (string, error) {
	out := body
	for i, e := range edits {
		n := strings.Count(out, e.Old)
		switch {
		case n == 0:
			return "", fmt.Errorf("edits[%d]: old_string not found in the current body -- "+
				"read the item again and copy the text verbatim, including whitespace and indentation", i)
		case n > 1 && !e.ReplaceAll:
			return "", fmt.Errorf("edits[%d]: old_string matches %d places; include more surrounding "+
				"text to make it unique, or pass replace_all: true to change all %d", i, n, n)
		case e.ReplaceAll:
			out = strings.ReplaceAll(out, e.Old, e.New)
		default:
			out = strings.Replace(out, e.Old, e.New, 1)
		}
	}
	if out == body {
		return "", errors.New("the edits produced no change to the body")
	}
	return out, nil
}

// unifiedDiff renders a line-level unified diff of before -> after, so the tool
// response shows what actually landed rather than making the agent re-read to
// find out. It is advisory: the authoritative answer is the returned
// content_hash.
//
// Hand-rolled on purpose -- an LCS over lines is a dozen lines of code, and the
// alternative is a new module dependency for one cosmetic string in one
// response. The output is capped (maxEditDiffRunes) with an explicit truncation
// marker; a silently shortened diff would read as a complete one.
func unifiedDiff(before, after string) string {
	a, b := splitLinesKeepEnd(before), splitLinesKeepEnd(after)
	var sb strings.Builder
	for _, h := range diffHunks(a, b, 3) {
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", h.aStart+1, h.aLen, h.bStart+1, h.bLen)
		for _, line := range h.lines {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	return truncateDiff(sb.String())
}

// truncateDiff caps the rendered diff, saying so when it cuts.
func truncateDiff(d string) string {
	r := []rune(d)
	if len(r) <= maxEditDiffRunes {
		return d
	}
	return string(r[:maxEditDiffRunes]) + "\n... diff truncated; the content_hash above is authoritative\n"
}

// splitLinesKeepEnd splits into lines without the trailing newline, treating a
// trailing newline as ending the last line rather than starting an empty one.
func splitLinesKeepEnd(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// hunk is one contiguous region of change plus its context lines.
type hunk struct {
	aStart, aLen int
	bStart, bLen int
	lines        []string
}

// diffHunks builds unified-diff hunks from an LCS table over lines, with the
// given number of context lines around each change.
func diffHunks(a, b []string, context int) []hunk {
	ops := diffOps(a, b)
	var hunks []hunk
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		// Walk back over up to `context` equal lines, and forward to the end of
		// this run of changes plus its trailing context.
		start := i
		for start > 0 && ops[start-1].kind == ' ' && i-start < context {
			start--
		}
		end := i
		gap := 0
		for end < len(ops) && gap <= context*2 {
			if ops[end].kind == ' ' {
				gap++
			} else {
				gap = 0
			}
			end++
		}
		for end > start && ops[end-1].kind == ' ' && countTrailingEqual(ops[start:end]) > context {
			end--
		}
		h := hunk{aStart: ops[start].aIndex, bStart: ops[start].bIndex}
		for _, op := range ops[start:end] {
			switch op.kind {
			case ' ':
				h.aLen++
				h.bLen++
				h.lines = append(h.lines, " "+op.text)
			case '-':
				h.aLen++
				h.lines = append(h.lines, "-"+op.text)
			case '+':
				h.bLen++
				h.lines = append(h.lines, "+"+op.text)
			}
		}
		hunks = append(hunks, h)
		i = end
	}
	return hunks
}

// countTrailingEqual counts the unchanged lines at the end of an op run.
func countTrailingEqual(ops []diffOp) int {
	n := 0
	for i := len(ops) - 1; i >= 0 && ops[i].kind == ' '; i-- {
		n++
	}
	return n
}

// diffOp is one line of the diff: kept (' '), removed ('-'), or added ('+'),
// with the index it occupies in the side(s) it belongs to.
type diffOp struct {
	kind           byte
	text           string
	aIndex, bIndex int
}

// diffOps computes the line-level edit script via a standard LCS table. Bodies
// here are a few hundred lines at most, so the O(n*m) table is the right
// trade: it is exact, and it is short enough to read.
func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{kind: ' ', text: a[i], aIndex: i, bIndex: j})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{kind: '-', text: a[i], aIndex: i, bIndex: j})
			i++
		default:
			ops = append(ops, diffOp{kind: '+', text: b[j], aIndex: i, bIndex: j})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{kind: '-', text: a[i], aIndex: i, bIndex: j})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{kind: '+', text: b[j], aIndex: i, bIndex: j})
	}
	return ops
}
