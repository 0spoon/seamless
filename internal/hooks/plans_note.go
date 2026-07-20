package hooks

// The cc-plan note upsert: one note per plan file, carrying the plan across its
// draft -> presented -> approved lifecycle without losing the composition slug,
// the owner's tags, or the iteration count.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/validate"
)

// planUpsert is upsertPlanNote's result: the written note, its composition
// slug, and whether this was the session's first plan capture (no plan_capture
// metadata existed yet) -- the trigger for the related-knowledge injection.
type planUpsert struct {
	note     core.Note
	planSlug string
	first    bool
}

// upsertPlanNote creates or updates the cc-plan-<basename> note for one plan
// iteration or approval. On update the note id, created time, plan:<slug> tag
// (the composition slug is minted once, at first capture), and any tags the
// owner or another agent added are preserved; the title, body, and the managed
// tags follow the latest content. An approval with
// no readable content flips the status of an existing note without touching
// its body; with no existing note it is dropped (fail-open). Agent-cache notes
// captured before the plan existed (pending on the session) are adopted into
// the composition here, once the slug is minted.
func (h *Handler) upsertPlanNote(ctx context.Context, p toolPayload, basename, content string, approve bool) (planUpsert, bool) {
	project := h.resolveProject(ctx, p.CWD)
	noteSlug := plans.NotePrefix + basename
	existing, found := h.loadNoteBySlug(ctx, project, noteSlug)

	trimmed := strings.TrimSpace(content)
	if trimmed == "" && !(approve && found) {
		return planUpsert{}, false
	}

	now := time.Now().UTC()
	note := existing
	iter := 1
	if found {
		iter = plans.NoteIteration(existing)
		if !approve && trimmed != "" {
			iter++
		}
	} else {
		id, err := core.NewID()
		if err != nil {
			h.logger.Warn("hooks: plan note id", "error", err)
			return planUpsert{}, false
		}
		note = core.Note{ID: id, Slug: noteSlug, Project: project, Created: now}
	}

	status := plans.StatusDraft
	if found && plans.StatusFromTags(existing.Tags) == plans.StatusApproved {
		status = plans.StatusApproved // an approved plan never regresses to draft
	}
	if approve {
		status = plans.StatusApproved
	}

	planSlug := plans.SlugFromTags(note.Tags)
	if trimmed != "" {
		title := firstHeading(content)
		if title == "" || validate.Title(title) != nil {
			title = basename
		}
		note.Title = title
		note.Body = planStamp(
			h.ambientDisplayName(ctx, ClientClaudeCode, p.SessionID),
			basename, iter, gitHead(p.CWD), now,
		) + "\n\n" + content
		// New plan content is attributed to the capturing session's model; an
		// unknown model keeps the note's prior attribution.
		if m := h.ambientModel(ctx, ClientClaudeCode, p.SessionID); m != "" {
			note.Model = m
		}
	}
	if planSlug == "" {
		planSlug = core.Slugify(note.Title)
	}
	note.Description = plans.NoteDescription(basename, iter, status)
	note.Tags = plans.SetStatusTag(mergePlanTags(note.Tags, planSlug), status)
	note.Updated = now
	if note.Extra == nil {
		note.Extra = map[string]any{}
	}
	note.Extra["plan_iteration"] = iter

	written, err := h.files.WriteNote(ctx, note)
	if err != nil {
		h.logger.Warn("hooks: plan note write", "slug", noteSlug, "error", err)
		return planUpsert{}, false
	}

	// Adopt agent-cache notes that completed before this plan existed: the
	// session accrued their slugs while no plan slug was known; tag them into
	// the now-minted composition and clear the pending list (the fresh meta
	// below carries none).
	prior, _ := h.sessionPlanMeta(ctx, p.SessionID)
	adopted := h.adoptPendingAgents(ctx, project, planSlug, prior.PendingAgents)
	h.setSessionPlanMeta(ctx, p.SessionID, planCaptureMeta{Basename: basename, PlanSlug: planSlug, Status: status})

	kind := core.EventPlanCaptured
	if approve {
		kind = core.EventPlanApproved
	}
	payload := map[string]any{
		"basename": basename, "plan_slug": planSlug, "iteration": iter,
		"title": events.Truncate(written.Title, h.maxEventChars),
	}
	if trimmed != "" {
		payload["content"] = content // verbatim, unbounded by design
	}
	if adopted > 0 {
		payload["adopted_agents"] = adopted
	}
	h.recordPlanEvent(ctx, kind, p.SessionID, written.ID, payload)
	return planUpsert{note: written, planSlug: planSlug, first: prior.Basename == ""}, true
}

// mergePlanTags rebuilds a captured-plan note's tag set for an upsert. The
// hook-managed tags stay authoritative and deduplicated -- plan:<slug> (the
// composition tag, replacing any other plan:* tag), cc-plan, and
// created-by:agent -- while every other tag already on the note (owner- or
// agent-added) is preserved in order, so a re-captured iteration never wipes
// them. The plan-status:* tag is managed separately via plans.SetStatusTag.
func mergePlanTags(existing []string, planSlug string) []string {
	managed := []string{plans.SlugTag(planSlug), plans.TagPlan, "created-by:agent"}
	out := make([]string, 0, len(managed)+len(existing))
	out = append(out, managed...)
	for _, t := range existing {
		if strings.HasPrefix(t, plans.SlugTagPrefix()) || slices.Contains(managed, t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// adoptPendingAgents adds the plan:<slug> tag to agent-cache notes captured
// before the plan's first iteration (the explore-first pattern: subagents
// finish before any plan file exists). Best-effort per note; one that vanished
// or already belongs to a plan is skipped. Returns how many were adopted.
func (h *Handler) adoptPendingAgents(ctx context.Context, project, planSlug string, slugs []string) int {
	adopted := 0
	for _, slug := range slugs {
		note, found := h.loadNoteBySlug(ctx, project, slug)
		if !found || plans.SlugFromTags(note.Tags) != "" {
			continue
		}
		note.Tags = append([]string{plans.SlugTag(planSlug)}, note.Tags...)
		note.Updated = time.Now().UTC()
		if _, err := h.files.WriteNote(ctx, note); err != nil {
			h.logger.Warn("hooks: adopt pending agent note", "slug", slug, "error", err)
			continue
		}
		adopted++
	}
	return adopted
}

// ensurePlanTask creates the "Implement plan" tracking task for an approved
// plan unless the plan already has an open or in-progress step (idempotent on
// re-approval).
func (h *Handler) ensurePlanTask(ctx context.Context, p toolPayload, note core.Note, planSlug string) {
	createdBy := ""
	if p.SessionID != "" {
		// Plan capture is Claude Code-only (Codex registers no plan-capture hooks).
		createdBy = h.ambientDisplayName(ctx, ClientClaudeCode, p.SessionID)
	}
	task, created, err := plans.EnsureTask(ctx, h.db, note, planSlug, createdBy)
	if err != nil {
		h.logger.Warn("hooks: plan task", "error", err)
		return
	}
	if !created {
		return
	}
	h.recordPlanEvent(ctx, core.EventTaskTransition, p.SessionID, task.ID, map[string]any{
		"to": string(core.TaskOpen), "created": true, "plan_slug": planSlug,
	})
}

// relatedPlanHits caps how many recall hits the first-capture injection lists.
const relatedPlanHits = 5

// relatedPlanContext builds the additionalContext block returned on a
// session's first captured plan iteration: top recall hits for the plan title
// (prior plans, constraints, related notes), so the planning agent sees prior
// art before the plan is finalized. Returns "" when recall is unavailable,
// errors, or finds nothing beyond the plan's own note; a non-empty block is
// also recorded as a retrieval.injected event.
func (h *Handler) relatedPlanContext(ctx context.Context, p toolPayload, note core.Note) string {
	if h.retrieve == nil || strings.TrimSpace(note.Title) == "" {
		return ""
	}
	hits, err := h.retrieve.Recall(ctx, retrieve.RecallInput{
		Query: note.Title, Project: note.Project, Limit: relatedPlanHits + 1,
	})
	if err != nil {
		h.logger.Warn("hooks: related plan recall", "error", err)
		return ""
	}
	var b strings.Builder
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		if hit.ID == note.ID {
			continue // the plan's own freshly-written note
		}
		if len(ids) == relatedPlanHits {
			break
		}
		read := "memory_read name=" + hit.Name
		if hit.Kind == "note" {
			read = "notes_read id=" + hit.ID
		}
		fmt.Fprintf(&b, "\n- [%s] %s (%s): %s -- %s", hit.Kind, hit.Title, hit.Age, hit.Description, read)
		ids = append(ids, hit.ID)
	}
	if len(ids) == 0 {
		return ""
	}
	block := "<seam-plan-context>\nSeamless has prior knowledge related to this plan; check before finalizing:" +
		b.String() + "\n</seam-plan-context>"
	// Plan capture is Claude Code-only (Codex registers no plan-capture hooks).
	h.recordInjection(ctx, "post-tool-use", ClientClaudeCode, p.SessionID, "", block, ids)
	return block
}

// recordPlanEvent appends a plan-capture event, attributed to the ambient
// session (best-effort) with the Claude session id riding in the payload.
func (h *Handler) recordPlanEvent(ctx context.Context, kind core.EventKind, claudeSessionID, itemID string, payload map[string]any) {
	if h.events == nil {
		return
	}
	// Plan capture is Claude Code-only (Codex registers no plan-capture hooks).
	sessionID, project := h.ambientRef(ctx, ClientClaudeCode, claudeSessionID)
	payload["claude_session_id"] = claudeSessionID
	payload["external_client"] = ClientClaudeCode.externalIdentity()
	if _, err := h.events.Record(ctx, core.Event{
		Kind: kind, SessionID: sessionID, ProjectSlug: project, ItemID: itemID, Payload: payload,
	}); err != nil {
		h.logger.Warn("hooks: record plan event", "kind", kind, "error", err)
	}
}
