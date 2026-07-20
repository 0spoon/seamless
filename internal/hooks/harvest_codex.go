package hooks

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// maxRolloutTailBytes bounds how much of a Codex rollout file the Stop harvest
// reads. The last agent message of a turn is one of the final lines (the
// task_complete event closes the turn), so a tail read finds it without parsing a
// session that may have grown to many MB. Generous enough that the closing events
// of a turn are always whole within it.
const maxRolloutTailBytes = 256 * 1024

// codexStopFindings is the findings text for one Codex Stop: the last agent
// message, prefixed and capped like the CC harvest ([[harvest.go]]) so the console
// and briefing read the two clients' findings the same way. It prefers the
// last_assistant_message the Stop payload carries (live-verified through
// codex-cli 0.144.6) and falls back to tail-parsing the rollout file only when
// the payload omits it (an older Codex, or a turn that ended without one). It
// returns "" when there is nothing to harvest, so the caller leaves any prior
// turn's findings intact rather than blanking them -- Stop fires every turn, and
// only a turn with real assistant text should move the provisional summary.
func codexStopFindings(lastAssistantMessage, transcriptPath string) string {
	msg := strings.TrimSpace(lastAssistantMessage)
	if msg == "" {
		msg = strings.TrimSpace(tailCodexRollout(transcriptPath))
	}
	if msg == "" {
		return ""
	}
	return "(auto-harvested) " + capRunes(msg, maxHarvestRunes)
}

// tailCodexRollout returns the last agent message from a Codex rollout JSONL file,
// reading only its final maxRolloutTailBytes. Any problem -- blank path, unreadable
// or moved file, no agent message in the tail -- yields "" (the caller degrades to
// a heartbeat-only Stop). It never errors: the Stop hook must always complete.
func tailCodexRollout(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return ""
	}
	start := int64(0)
	if info.Size() > maxRolloutTailBytes {
		start = info.Size() - maxRolloutTailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}

	lines := bytes.Split(data, []byte("\n"))
	// A mid-file seek almost always lands inside a line; drop that leading partial
	// so we never parse half a JSON object.
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	last := ""
	for _, line := range lines {
		if msg := codexRolloutLineMessage(line); msg != "" {
			last = msg
		}
	}
	return last
}

// codexRolloutEvent is the tolerant shape of one rollout JSONL line: a typed
// wrapper around a payload whose shape depends on the type.
type codexRolloutEvent struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// codexRolloutLineMessage returns the agent message a single rollout line carries,
// or "" if it carries none. Three shapes hold the final answer (in the order Codex
// writes them within a turn; the caller keeps the LAST match, so the turn-closing
// task_complete wins):
//   - event_msg / task_complete -> payload.last_agent_message (the turn's end marker)
//   - event_msg / agent_message -> payload.message (phase "final_answer")
//   - response_item / message, role assistant -> payload.content[].output_text.text
//
// Verified against the versioned v0.144.5 rollout fixture. Tolerant: a malformed
// or unrelated line is "".
func codexRolloutLineMessage(line []byte) string {
	if len(bytes.TrimSpace(line)) == 0 {
		return ""
	}
	var ev codexRolloutEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return ""
	}
	switch ev.Type {
	case "event_msg":
		var p struct {
			Type             string `json:"type"`
			LastAgentMessage string `json:"last_agent_message"` // task_complete
			Message          string `json:"message"`            // agent_message
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return ""
		}
		switch p.Type {
		case "task_complete":
			return p.LastAgentMessage
		case "agent_message":
			return p.Message
		}
	case "response_item":
		var p struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return ""
		}
		if p.Type != "message" || p.Role != "assistant" {
			return ""
		}
		var b strings.Builder
		for _, c := range p.Content {
			if c.Type == "output_text" && c.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(c.Text)
			}
		}
		return b.String()
	}
	return ""
}
