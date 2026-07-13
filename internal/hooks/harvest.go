package hooks

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
)

// harvestFallback is the finding recorded when no assistant text can be
// harvested (missing/unreadable/empty transcript). It marks the session ended
// without a summary rather than leaving findings blank.
const harvestFallback = "(auto) session ended, no summary harvested"

// maxHarvestRunes caps auto-harvested findings so a long final message does not
// bloat the session record or later briefings.
const maxHarvestRunes = 2000

// transcriptLine is the tolerant shape of one Claude Code transcript JSONL line.
// Only assistant lines carry the text we harvest; message.content is either a
// string or an array of typed blocks.
type transcriptLine struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// harvestFindings extracts draft findings from a Claude Code transcript: it
// returns the text of the LAST assistant message (its text blocks concatenated),
// prefixed "(auto-harvested) " and capped at maxHarvestRunes. Any problem --
// blank path, unreadable file, no assistant text -- yields harvestFallback. It
// never errors; the session-end hook must always complete the session.
func harvestFindings(path string) string {
	if strings.TrimSpace(path) == "" {
		return harvestFallback
	}
	f, err := os.Open(path)
	if err != nil {
		return harvestFallback
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	// Transcript lines can be large (long assistant turns, tool results); grow the
	// scanner buffer well past the 64 KiB default so a long line is not dropped.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	last := ""
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var tl transcriptLine
		if err := json.Unmarshal(line, &tl); err != nil {
			continue // tolerant: skip malformed lines
		}
		if tl.Type != "assistant" {
			continue
		}
		if txt := messageText(tl.Message.Content); txt != "" {
			last = txt
		}
	}

	last = strings.TrimSpace(last)
	if last == "" {
		return harvestFallback
	}
	return "(auto-harvested) " + capRunes(last, maxHarvestRunes)
}

// messageText concatenates the text blocks of a transcript message's content
// (assistant or user), which is either a plain string or an array of typed blocks.
func messageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" && blk.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(blk.Text)
			}
		}
		return strings.TrimSpace(b.String())
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

// capRunes truncates s to at most n runes (never splitting a multi-byte rune).
func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
