// Package plans holds the shared vocabulary of captured Claude Code plans:
// the note-slug prefixes, the plan-status tag lifecycle, and the tracking-task
// composition. The hooks capture path, the console Plans surface, the briefing
// assembler, and the gardener all speak this vocabulary; keeping it here stops
// the tag spellings from drifting apart.
package plans

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// NotePrefix + the CC plan file basename is the captured-plan note slug -- the
// correlation key across iterations (a direct NoteBySlug lookup, no tag query).
const NotePrefix = "cc-plan-"

// AgentNotePrefix + the CC agent id is the agent-cache note slug.
const AgentNotePrefix = "cc-agent-"

// Marker tags on captured notes: TagPlan marks the plan note itself, TagAgent
// the cached planning-subagent runs attached to it.
const (
	TagPlan  = "cc-plan"
	TagAgent = "agent-cache"
)

// Plan lifecycle statuses, stored as a plan-status:<v> note tag. Draft ->
// presented -> approved is the capture flow; abandoned is the gardener's
// terminal state for plans that were never approved.
const (
	StatusDraft     = "draft"
	StatusPresented = "presented"
	StatusApproved  = "approved"
	StatusAbandoned = "abandoned"
)

// statusTagPrefix prefixes the lifecycle tag value.
const statusTagPrefix = "plan-status:"

// slugTagPrefix prefixes the composition tag value.
const slugTagPrefix = "plan:"

// SlugFromTags returns the plan:<slug> composition slug, or "".
func SlugFromTags(tags []string) string {
	for _, t := range tags {
		if after, ok := strings.CutPrefix(t, slugTagPrefix); ok {
			return after
		}
	}
	return ""
}

// StatusFromTags returns the plan-status:<v> value, or "".
func StatusFromTags(tags []string) string {
	for _, t := range tags {
		if after, ok := strings.CutPrefix(t, statusTagPrefix); ok {
			return after
		}
	}
	return ""
}

// SetStatusTag replaces the plan-status:<v> tag, preserving all others.
func SetStatusTag(tags []string, status string) []string {
	out := make([]string, 0, len(tags)+1)
	for _, t := range tags {
		if !strings.HasPrefix(t, statusTagPrefix) {
			out = append(out, t)
		}
	}
	return append(out, statusTagPrefix+status)
}

// SlugTag renders the composition tag for a plan slug.
func SlugTag(planSlug string) string { return slugTagPrefix + planSlug }

// SlugTagPrefix is the literal prefix every composition tag carries, for
// surfaces that enumerate the plan:<slug> family without a known slug.
func SlugTagPrefix() string { return slugTagPrefix }

// Basename recovers the CC plan file basename from a captured-plan note slug.
func Basename(noteSlug string) string { return strings.TrimPrefix(noteSlug, NotePrefix) }

// NoteIteration reads a captured-plan note's plan_iteration frontmatter
// (preserved in Extra), tolerating the numeric types YAML/JSON round-trips
// produce.
func NoteIteration(n core.Note) int {
	switch v := n.Extra["plan_iteration"].(type) {
	case int:
		return max(v, 1)
	case int64:
		return max(int(v), 1)
	case float64:
		return max(int(v), 1)
	case string:
		if i, err := strconv.Atoi(v); err == nil {
			return max(i, 1)
		}
	}
	return 1
}

// NoteDescription is the captured-plan note's one-line index text.
func NoteDescription(basename string, iter int, status string) string {
	return fmt.Sprintf("Captured Claude Code plan %s.md (iteration %d, %s)", basename, iter, status)
}

// EarlierPrimary reports whether a should win over b as a plan's narrative
// primary: the earlier Created wins, ties broken by the lower id for stability.
func EarlierPrimary(a, b core.Note) bool {
	if !a.Created.Equal(b.Created) {
		return a.Created.Before(b.Created)
	}
	return a.ID < b.ID
}

// Composition resolves a plan slug to its primary note and the other notes
// tagged into the composition (agent caches and supporting notes). The primary
// is the cc-plan capture when one carries the tag; otherwise it is the composed
// plan's narrative -- the earliest-created non-agent note (EarlierPrimary). ok
// is false when no note carries the tag at all -- a task-only plan has no
// composition and, notably, nothing to hang a favorite on.
func Composition(ctx context.Context, db *sql.DB, slug string) (primary core.Note, others []core.Note, ok bool, err error) {
	tagged, err := store.NotesByTag(ctx, db, "", SlugTag(slug))
	if err != nil {
		return core.Note{}, nil, false, err
	}
	idx := -1
	for i, n := range tagged {
		if slices.Contains(n.Tags, TagPlan) {
			idx = i
			break
		}
	}
	if idx == -1 {
		for i, n := range tagged {
			if slices.Contains(n.Tags, TagAgent) {
				continue
			}
			if idx == -1 || EarlierPrimary(n, tagged[idx]) {
				idx = i
			}
		}
	}
	if idx == -1 {
		return core.Note{}, nil, false, nil
	}
	others = make([]core.Note, 0, len(tagged)-1)
	for i, n := range tagged {
		if i != idx {
			others = append(others, n)
		}
	}
	return tagged[idx], others, true, nil
}

// EnsureTask creates the "Implement plan" tracking task for an approved plan
// unless the plan already has an open or in-progress step (idempotent on
// re-approval). It returns the created task and true, or a zero task and false
// when one already exists. Event recording is the caller's job (hooks and the
// console attribute differently).
func EnsureTask(ctx context.Context, db *sql.DB, note core.Note, planSlug, createdBy string) (core.Task, bool, error) {
	tasks, err := store.ListTasksForPlan(ctx, db, note.Project, "", planSlug)
	if err != nil {
		return core.Task{}, false, err
	}
	for _, t := range tasks {
		if !t.Status.Closed() {
			return core.Task{}, false, nil
		}
	}
	id, err := core.NewID()
	if err != nil {
		return core.Task{}, false, err
	}
	now := time.Now().UTC()
	task := core.Task{
		ID: id, ProjectSlug: note.Project,
		Title: "Implement plan: " + note.Title,
		Body: fmt.Sprintf("Plan note: %s (id %s). Agent caches: notes tagged plan:%s. "+
			"Check git stamps for staleness before trusting caches.", note.Slug, note.ID, planSlug),
		Status: core.TaskOpen, CreatedBy: createdBy, PlanSlug: planSlug,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateTask(ctx, db, task); err != nil {
		return core.Task{}, false, err
	}
	return task, true, nil
}
