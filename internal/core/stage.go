package core

import "strings"

// Stage memories open their body with a lightweight machine-readable header:
// "Status: open|in_progress|blocked|done" and "Gate: human|ai" lines. The
// briefing pins a stage while its status marks a live gate; the gardener
// proposes archiving one that no longer does.

// StageStatusDone marks a completed stage: it leaves the briefing immediately
// and the gardener may propose archiving it once it has sat unchanged.
const StageStatusDone = "done"

// stageHeaderScanLines bounds how far into a stage body ParseStageHeader looks
// for the Status/Gate lines; the convention puts them at the very top.
const stageHeaderScanLines = 12

// ParseStageHeader extracts the Status and Gate values from a stage memory body.
// The convention: the body opens with "Status: open|in_progress|blocked|done"
// and "Gate: human|ai" lines (case-insensitive, order-independent, leading
// markdown like "- ", "**", or "## " tolerated). Missing fields come back "".
func ParseStageHeader(body string) (status, gate string) {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if i >= stageHeaderScanLines {
			break
		}
		l := strings.TrimSpace(line)
		l = strings.TrimLeft(l, "-*># \t")
		if v, ok := headerValue(l, "status:"); ok && status == "" {
			status = v
		}
		if v, ok := headerValue(l, "gate:"); ok && gate == "" {
			gate = v
		}
		if status != "" && gate != "" {
			break
		}
	}
	return status, gate
}

// StageStatusLive reports whether a parsed stage status marks a live gate:
// open, in_progress, or blocked. Done, absent, and unrecognized statuses are
// all non-live -- the briefing ages such stages out of pinning and the
// gardener proposes archiving them, so a stage only holds its permanent pin by
// actually carrying a gate.
func StageStatusLive(status string) bool {
	switch status {
	case "open", "in_progress", "blocked":
		return true
	}
	return false
}

// headerValue returns the trimmed, lowercased value after a case-insensitive
// prefix, and whether the line had that prefix.
func headerValue(line, prefix string) (string, bool) {
	if len(line) < len(prefix) || !strings.EqualFold(line[:len(prefix)], prefix) {
		return "", false
	}
	v := strings.TrimSpace(line[len(prefix):])
	// Keep only the first token (drop trailing prose/markdown after the value).
	if fields := strings.Fields(v); len(fields) > 0 {
		v = fields[0]
	}
	return strings.ToLower(strings.Trim(v, "*`")), true
}
