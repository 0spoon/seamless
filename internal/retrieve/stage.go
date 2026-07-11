package retrieve

import (
	"strings"

	"github.com/0spoon/seamless/internal/core"
)

// MemoryBodyReader reads a full memory (including its body) from a data-dir-
// relative file path. *files.Store satisfies it. It is optional on the Service:
// when unset, the briefing omits the pinned-stage section (index rows carry no
// body, so stage status cannot be parsed without it).
type MemoryBodyReader interface {
	ReadMemory(relPath string) (core.Memory, error)
}

// SetBodyReader enables the pinned-stage briefing section by giving the Service a
// way to read stage memory bodies (their Status/Gate header lives in the body).
func (s *Service) SetBodyReader(r MemoryBodyReader) { s.bodyReader = r }

// stageLine is one pinned stage rendered in the briefing.
type stageLine struct {
	name   string
	status string
	gate   string
}

// stageHeaderScanLines bounds how far into a stage body ParseStageHeader looks
// for the Status/Gate lines; the convention puts them at the very top.
const stageHeaderScanLines = 12

// ParseStageHeader extracts the Status and Gate values from a stage memory body.
// The convention: the body opens with "Status: open|in_progress|blocked|done"
// and "Gate: human|ai" lines (case-insensitive, order-independent, leading
// markdown like "- " or "**" tolerated). Missing fields come back "".
func ParseStageHeader(body string) (status, gate string) {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if i >= stageHeaderScanLines {
			break
		}
		l := strings.TrimSpace(line)
		l = strings.TrimLeft(l, "-*> \t")
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

// pinnedStages reads each stage memory's body, parses its Status/Gate header, and
// returns the non-done stages to pin in the briefing (newest-updated first, as
// passed in). It returns nil when no body reader is configured, or a stage's
// file cannot be read (best-effort; a missing stage body must not break the
// briefing).
func (s *Service) pinnedStages(stages []core.Memory) []stageLine {
	if s.bodyReader == nil || len(stages) == 0 {
		return nil
	}
	var out []stageLine
	for _, m := range stages {
		mem, err := s.bodyReader.ReadMemory(m.FilePath)
		if err != nil {
			s.logger.Warn("retrieve: read stage body", "name", m.Name, "error", err)
			continue
		}
		status, gate := ParseStageHeader(mem.Body)
		if status == "done" {
			continue // completed stages are not pinned
		}
		out = append(out, stageLine{name: m.Name, status: status, gate: gate})
	}
	return out
}

// stageHead renders the pinned-stage lines for the briefing header (right after
// constraints), or "" when there are none.
func stageHead(stages []stageLine) string {
	if len(stages) == 0 {
		return ""
	}
	var b strings.Builder
	for _, st := range stages {
		b.WriteString("STAGE: " + sanitizeField(st.name, 80) + " -- ")
		if st.status != "" {
			b.WriteString(sanitizeField(st.status, 40))
		} else {
			b.WriteString("status unknown")
		}
		if st.gate != "" {
			b.WriteString(", gate " + sanitizeField(st.gate, 40))
		}
		b.WriteString("\n")
	}
	return b.String()
}
