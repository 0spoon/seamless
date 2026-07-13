package hooks

// Planning-subagent capture. SubagentStop fires when an Agent-tool subagent
// completes; during plan-mode activity its transcript (first user message =
// the prompt, last assistant text = the final report) is cached as a
// cc-agent-<agent_id> note tagged into the session's plan composition, so an
// implementing agent inherits the exploration without re-running it.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
)

// agentNotePrefix + the CC agent id is the agent-cache note slug.
const agentNotePrefix = "cc-agent-"

// maxAgentTitleRunes caps the prompt-derived part of an agent-cache note title.
const maxAgentTitleRunes = 120

func (h *Handler) subagentStop(w http.ResponseWriter, r *http.Request) {
	if !verifyBearer(r, h.apiKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBody)
	var p toolPayload
	_ = json.NewDecoder(r.Body).Decode(&p)

	if h.captureEnabled() {
		ctx, cancel := context.WithTimeout(r.Context(), captureTimeout)
		defer cancel()
		h.captureSubagent(ctx, p)
	}
	writeHookAck(w)
}

// captureSubagent caches a completed subagent's prompt and report as a note.
// Gate: only while the session has an unapproved plan capture or is in plan
// mode -- otherwise every subagent machine-wide would produce a note.
func (h *Handler) captureSubagent(ctx context.Context, p toolPayload) {
	if p.AgentID == "" {
		return
	}
	meta, hasMeta := h.sessionPlanMeta(ctx, p.SessionID)
	planning := hasMeta && (meta.Status == planStatusDraft || meta.Status == planStatusPresented)
	if !planning && p.PermissionMode != "plan" {
		return
	}
	prompt, report := parseSubagentTranscript(subagentTranscriptPath(p))
	if prompt == "" && report == "" {
		h.logger.Warn("hooks: subagent transcript unreadable or empty", "agent_id", p.AgentID)
		return
	}

	project := h.resolveProject(ctx, p.CWD)
	noteSlug := agentNotePrefix + core.Slugify(p.AgentID)
	now := time.Now().UTC()

	note, found := h.loadNoteBySlug(ctx, project, noteSlug)
	if !found {
		id, err := core.NewID()
		if err != nil {
			h.logger.Warn("hooks: agent note id", "error", err)
			return
		}
		note = core.Note{ID: id, Slug: noteSlug, Project: project, Created: now}
	}
	note.Title = agentNoteTitle(p.AgentType, prompt)
	note.Description = fmt.Sprintf("Cached planning-subagent run (%s) -- prompt + final report", p.AgentType)
	note.Body = agentStamp(p.SessionID, p.AgentID, gitHead(p.CWD), now) +
		"\n\n## Prompt\n\n" + prompt + "\n\n## Report\n\n" + report
	note.Tags = agentNoteTags(meta.PlanSlug, p.AgentType)
	note.Updated = now

	written, err := h.files.WriteNote(ctx, note)
	if err != nil {
		h.logger.Warn("hooks: agent note write", "slug", noteSlug, "error", err)
		return
	}
	h.recordPlanEvent(ctx, core.EventSubagentCaptured, p.SessionID, written.ID, map[string]any{
		"content": report, // verbatim, unbounded by design
		"prompt":  events.Truncate(prompt, h.maxEventChars),
		"agent_id": p.AgentID, "agent_type": p.AgentType, "plan_slug": meta.PlanSlug,
	})
}

// agentNoteTags composes the agent-cache note's tag set; the plan tag is
// omitted when the session has no correlated plan (pure plan-mode gate).
func agentNoteTags(planSlug, agentType string) []string {
	tags := make([]string, 0, 4)
	if planSlug != "" {
		tags = append(tags, "plan:"+planSlug)
	}
	tags = append(tags, "agent-cache")
	if agentType != "" {
		tags = append(tags, "agent:"+agentType)
	}
	return append(tags, "created-by:agent")
}

// agentNoteTitle is "[<type>] <first prompt line>", capped.
func agentNoteTitle(agentType, prompt string) string {
	line := prompt
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = "subagent run"
	}
	line = capRunes(line, maxAgentTitleRunes)
	if agentType == "" {
		return line
	}
	return "[" + agentType + "] " + line
}

// agentStamp is the provenance blockquote prepended to an agent-cache note.
func agentStamp(claudeSessionID, agentID, head string, now time.Time) string {
	return fmt.Sprintf("> captured from %s | agent %s | git %s | %s",
		stampSession(claudeSessionID), agentID, shortHead(head), now.UTC().Format(time.RFC3339))
}

// subagentTranscriptPath resolves the subagent's JSONL transcript: the payload
// transcript_path when it already names a subagents/agent-*.jsonl file, else
// constructed from the main transcript path and agent id (the verified layout:
// <proj-dir>/<session-id>/subagents/agent-<agent_id>.jsonl).
func subagentTranscriptPath(p toolPayload) string {
	if p.TranscriptPath == "" {
		return ""
	}
	base := filepath.Base(p.TranscriptPath)
	if filepath.Base(filepath.Dir(p.TranscriptPath)) == "subagents" &&
		strings.HasPrefix(base, "agent-") && strings.HasSuffix(base, ".jsonl") {
		return p.TranscriptPath
	}
	return filepath.Join(strings.TrimSuffix(p.TranscriptPath, ".jsonl"),
		"subagents", "agent-"+p.AgentID+".jsonl")
}

// parseSubagentTranscript extracts the prompt (first user message) and final
// report (last assistant text) from a subagent transcript. Any problem yields
// empty strings; capture is best-effort and never errors.
func parseSubagentTranscript(path string) (prompt, report string) {
	if strings.TrimSpace(path) == "" {
		return "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	// Same headroom as harvestFindings: transcript lines can be very large.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var tl transcriptLine
		if err := json.Unmarshal(line, &tl); err != nil {
			continue // tolerant: skip malformed lines
		}
		switch tl.Type {
		case "user":
			if prompt == "" {
				prompt = messageText(tl.Message.Content)
			}
		case "assistant":
			if txt := messageText(tl.Message.Content); txt != "" {
				report = txt
			}
		}
	}
	return strings.TrimSpace(prompt), strings.TrimSpace(report)
}
