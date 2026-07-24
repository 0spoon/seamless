package hooks

// Subagent lifecycle handling. SubagentStart fires for both clients and
// injects the same child briefing -- pinned constraints, up to three RELEVANT
// memories matched from the child's spawn prompt, and the recall/memory_read
// footer -- while sharing its parent's ambient session. Claude Code emits no
// SessionStart for Task subagents, so this is a CC child's only injection
// point.
// Codex SubagentStop is deliberately limited to a
// parent heartbeat: it never harvests child output into the parent's findings or
// creates durable notes. Claude Code's established planning-subagent capture is
// kept separate and unchanged: during plan-mode activity its transcript (first
// user message = prompt, last assistant text = final report) is cached as a
// cc-agent-<agent_id> note in the session's plan composition.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/retrieve"
)

// maxAgentTitleRunes caps the prompt-derived part of an agent-cache note title.
const maxAgentTitleRunes = 120

// subagentStart injects the child briefing (project constraints plus
// spawn-prompt-matched RELEVANT memories) without running any ambient-session
// ensure/reactivation path. Both contracts (the captured Codex fixtures and
// Claude Code's documented SubagentStart payload) name session_id as the
// parent id, so a heartbeat is safe; model attribution is intentionally not
// updated because a child may run a different model.
func (h *Handler) subagentStart(w http.ResponseWriter, r *http.Request) {
	if !verifyBearer(r, h.apiKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	client, ok := requireRequestClient(w, r)
	if !ok {
		return
	}
	p := decodeSubagentStart(client, readHookBody(w, r))

	ctx, cancel := context.WithTimeout(r.Context(), hookTimeout)
	defer cancel()

	// The released contract requires these fields. A malformed request gets an
	// empty, correctly shaped response; never guess a parent, child, or scope.
	if strings.TrimSpace(p.ParentSessionID) == "" || strings.TrimSpace(p.AgentID) == "" ||
		strings.TrimSpace(p.AgentType) == "" || strings.TrimSpace(p.CWD) == "" {
		h.writeContextResponse(ctx, w, "SubagentStart", "subagent-start", client,
			p.ParentSessionID, "", "", false, nil)
		return
	}
	briefing, injectedIDs, err := h.retrieve.Briefing(ctx, subagentBriefingInput(client, p))
	if err != nil {
		h.logger.Warn("hooks: subagent-start briefing failed", "error", err)
		briefing, injectedIDs = "", nil
	}

	// This is the only parent-session mutation: no ensure, project registration,
	// re-scope, rename, model update, findings harvest, or completion occurs here.
	h.touchAmbient(ctx, client, p.ParentSessionID)
	h.writeContextResponse(ctx, w, "SubagentStart", "subagent-start", client,
		p.ParentSessionID, "", briefing, briefing != "", injectedIDs)
}

func (h *Handler) subagentStop(w http.ResponseWriter, r *http.Request) {
	if !verifyBearer(r, h.apiKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	client, ok := requireRequestClient(w, r)
	if !ok {
		return
	}
	p := decodeSubagentStop(client, readHookBody(w, r))

	if client == ClientCodex {
		// The Codex contract shares the parent session_id. Keep it alive, but do
		// not apply the child model/final message to the parent and do not enter
		// Claude's plan-note capture path.
		ctx, cancel := context.WithTimeout(r.Context(), hookTimeout)
		h.touchAmbient(ctx, client, p.ParentSessionID)
		cancel()
		writeHookAck(w)
		return
	}

	if h.captureEnabled() {
		ctx, cancel := context.WithTimeout(r.Context(), captureTimeout)
		defer cancel()
		h.captureSubagent(ctx, p)
	}
	writeHookAck(w)
}

// captureSubagent caches a completed Claude Code subagent's prompt and report as
// a note.
// Gate: only while the session has an unapproved plan capture or is in plan
// mode -- otherwise every subagent machine-wide would produce a note.
func (h *Handler) captureSubagent(ctx context.Context, p subagentPayload) {
	if p.AgentID == "" {
		return
	}
	meta, hasMeta := h.sessionPlanMeta(ctx, p.ParentSessionID)
	planning := hasMeta && (meta.Status == plans.StatusDraft || meta.Status == plans.StatusPresented)
	if !planning && p.PermissionMode != "plan" {
		return
	}
	prompt, report := parseSubagentTranscript(subagentTranscriptPath(p))
	if prompt == "" && report == "" {
		h.logger.Warn("hooks: subagent transcript unreadable or empty", "agent_id", p.AgentID)
		return
	}

	project := h.resolveProject(ctx, p.CWD)
	noteSlug := plans.AgentNotePrefix + core.Slugify(p.AgentID)
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
	note.Body = agentStamp(
		h.ambientDisplayName(ctx, ClientClaudeCode, p.ParentSessionID),
		p.AgentID, gitHead(p.CWD), now,
	) +
		"\n\n## Prompt\n\n" + prompt + "\n\n## Report\n\n" + report
	note.Tags = agentNoteTags(meta.PlanSlug, p.AgentType)
	note.Updated = now

	written, err := h.files.WriteNote(ctx, note)
	if err != nil {
		h.logger.Warn("hooks: agent note write", "slug", noteSlug, "error", err)
		return
	}
	// No plan slug yet (the explore-first pattern: subagents finish before the
	// first plan-file write): park the note slug on the session so the first
	// plan capture adopts it into the composition.
	if meta.PlanSlug == "" && !slices.Contains(meta.PendingAgents, noteSlug) {
		meta.PendingAgents = append(meta.PendingAgents, noteSlug)
		h.setSessionPlanMeta(ctx, p.ParentSessionID, meta)
	}
	h.recordPlanEvent(ctx, core.EventSubagentCaptured, p.ParentSessionID, written.ID, map[string]any{
		"content":  report, // verbatim, unbounded by design
		"prompt":   events.Truncate(prompt, h.maxEventChars),
		"agent_id": p.AgentID, "agent_type": p.AgentType, "plan_slug": meta.PlanSlug,
	})
}

// agentNoteTags composes the agent-cache note's tag set; the plan tag is
// omitted when the session has no correlated plan (pure plan-mode gate).
func agentNoteTags(planSlug, agentType string) []string {
	tags := make([]string, 0, 4)
	if planSlug != "" {
		tags = append(tags, plans.SlugTag(planSlug))
	}
	tags = append(tags, plans.TagAgent)
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
func agentStamp(sessionName, agentID, head string, now time.Time) string {
	return fmt.Sprintf("> captured from %s | agent %s | git %s | %s",
		stampSession(sessionName), agentID, shortHead(head), now.UTC().Format(time.RFC3339))
}

// subagentBriefingInput builds the child-briefing input from a SubagentStart
// payload, carrying the spawn prompt when it can be resolved. The briefing
// matches the prompt against the project's memories and renders the hits as
// its RELEVANT section; an unresolved (empty) prompt just means no section.
func subagentBriefingInput(client Client, p subagentPayload) retrieve.BriefingInput {
	return retrieve.BriefingInput{
		CWD:       p.CWD,
		AgentType: p.AgentType,
		Prompt:    subagentSpawnPrompt(client, p),
	}
}

// subagentSpawnPrompt best-effort resolves the child's spawn prompt at
// SubagentStart, per client: Claude Code = the first user message of the child
// transcript (which may not exist yet when the hook fires); Codex = the first
// user event of the child rollout that the Start payload's transcript_path
// names (Stop differs: there transcript_path is the parent rollout and
// agent_transcript_path the child). Every failure mode -- absent, empty, or
// unparseable file, prompt not yet flushed, oversized line -- yields ""
// silently; reads are size-bounded and never turn the hook response into an
// error.
func subagentSpawnPrompt(client Client, p subagentPayload) string {
	switch client {
	case ClientClaudeCode:
		prompt, _ := parseSubagentTranscript(subagentTranscriptPath(p))
		return prompt
	case ClientCodex:
		return headCodexRollout(p.TranscriptPath)
	}
	return ""
}

// subagentTranscriptPath resolves the subagent's JSONL transcript: the payload
// transcript_path when it already names a subagents/agent-*.jsonl file, else
// constructed from the main transcript path and agent id (the verified layout:
// <proj-dir>/<session-id>/subagents/agent-<agent_id>.jsonl). An agent id
// carrying path separators or ".." is rejected rather than joined into the
// path -- it is a filename fragment, never a path.
func subagentTranscriptPath(p subagentPayload) string {
	if p.TranscriptPath == "" {
		return ""
	}
	base := filepath.Base(p.TranscriptPath)
	if filepath.Base(filepath.Dir(p.TranscriptPath)) == "subagents" &&
		strings.HasPrefix(base, "agent-") && strings.HasSuffix(base, ".jsonl") {
		return p.TranscriptPath
	}
	if strings.ContainsAny(p.AgentID, `/\`) || strings.Contains(p.AgentID, "..") {
		return ""
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
