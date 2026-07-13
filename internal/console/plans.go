package console

// The Plans screen surfaces captured Claude Code plans (cc-plan notes): their
// lifecycle status, iteration count, cached subagent runs, and tracking tasks.
// It also carries the owner escape hatch for Claude Code bug #20397 (an
// approval whose PostToolUse never fired): POST /console/plans/{slug}/approve
// flips the note to approved and creates the tracking task, exactly as the
// hook would have.

import (
	"context"
	"html/template"
	"net/http"
	"slices"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/store"
)

// planRow is a display projection of one captured plan.
type planRow struct {
	NoteID     string    `json:"noteId"`
	Slug       string    `json:"slug"`     // plan:<slug> composition key
	Basename   string    `json:"basename"` // CC plan file name without .md
	Title      string    `json:"title"`
	Project    string    `json:"project,omitempty"`
	Status     string    `json:"status"`
	Iteration  int       `json:"iteration,omitempty"`
	Agents     int       `json:"agents"` // cached subagent notes in the composition
	TasksDone  int       `json:"tasksDone"`
	TasksTotal int       `json:"tasksTotal"`
	Updated    time.Time `json:"updated"`
}

// plansData is the Plans list payload.
type plansData struct {
	Rows        []planRow      `json:"rows"`
	Count       int            `json:"count"`
	Window      string         `json:"window"`
	WindowLabel string         `json:"windowLabel"`
	Windows     []windowOption `json:"-"`
}

// planNoteRef is a compact pointer to a note attached to a plan composition.
type planNoteRef struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Slug    string    `json:"slug"`
	IsAgent bool      `json:"isAgent"` // an agent-cache note (vs a supporting note)
	Updated time.Time `json:"updated"`
}

// planTaskRef is a compact pointer to a plan step task.
type planTaskRef struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// planDetailData is the single-plan payload: the row, the rendered plan body,
// and the composition's attached notes and tasks.
type planDetailData struct {
	Row        planRow       `json:"plan"`
	Body       template.HTML `json:"-"`
	BodyText   string        `json:"body"`
	BodyLoaded bool          `json:"bodyAvailable"`
	Attached   []planNoteRef `json:"attached,omitempty"`
	Tasks      []planTaskRef `json:"tasks,omitempty"`
	CanApprove bool          `json:"canApprove"`
}

func (s *Service) plansList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	win := store.ResolveRetrievalWindow(r.URL.Query().Get("w"), time.Now())
	notes, err := store.NotesByTag(ctx, s.cfg.DB, "", plans.TagPlan)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	agentCount, err := s.planAgentCounts(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rows := make([]planRow, 0, len(notes))
	for _, n := range notes {
		if !win.Since.IsZero() && n.Updated.Before(win.Since) {
			continue
		}
		rows = append(rows, s.planRow(ctx, n, agentCount))
	}
	s.render(w, r, "plans", pageData{Title: "Plans", Active: "plans", Data: plansData{
		Rows: rows, Count: len(rows),
		Window: win.Key, WindowLabel: win.Label, Windows: windowOptions(win.Key),
	}})
}

func (s *Service) planDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := r.PathValue("slug")
	planNote, attached, ok, err := s.planComposition(ctx, slug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	agentCount, err := s.planAgentCounts(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := planDetailData{Row: s.planRow(ctx, planNote, agentCount), Attached: attached}
	d.CanApprove = d.Row.Status != plans.StatusApproved
	if s.cfg.Files != nil {
		if full, ferr := s.cfg.Files.Store().ReadNote(planNote.FilePath); ferr != nil {
			s.logger.Warn("console: read plan body", "slug", slug, "error", ferr)
		} else {
			d.BodyText = full.Body
			d.BodyLoaded = true
			d.Body = s.renderBody(ctx, full.Body, planNote.Project)
		}
	}
	tasks, err := store.ListTasksForPlan(ctx, s.cfg.DB, planNote.Project, "", slug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, t := range tasks {
		d.Tasks = append(d.Tasks, planTaskRef{ID: t.ID, Title: t.Title, Status: string(t.Status)})
	}
	s.render(w, r, "plan", pageData{Title: "Plan " + d.Row.Title, Active: "plans", Data: d})
}

// planApprove is the owner escape hatch for Claude Code bug #20397 (approve +
// immediate /clear skips the ExitPlanMode PostToolUse): it flips the plan note
// to approved and ensures the tracking task, mirroring the hook path.
func (s *Service) planApprove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := r.PathValue("slug")
	planNote, _, ok, err := s.planComposition(ctx, slug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if s.cfg.Files == nil {
		http.Error(w, "files layer unavailable", http.StatusServiceUnavailable)
		return
	}
	note, err := s.cfg.Files.Store().ReadNote(planNote.FilePath)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	basename := plans.Basename(note.Slug)
	if plans.StatusFromTags(note.Tags) != plans.StatusApproved {
		note.Tags = plans.SetStatusTag(note.Tags, plans.StatusApproved)
		note.Description = plans.NoteDescription(basename, plans.NoteIteration(note), plans.StatusApproved)
		note.Updated = time.Now().UTC()
		if note, err = s.cfg.Files.WriteNote(ctx, note); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.recordPlanAction(ctx, core.EventPlanApproved, note.Project, note.ID, map[string]any{
			"basename": basename, "plan_slug": slug, "by": "console",
		})
	}
	task, created, err := plans.EnsureTask(ctx, s.cfg.DB, note, slug, "console")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if created {
		s.recordPlanAction(ctx, core.EventTaskTransition, note.Project, task.ID, map[string]any{
			"to": string(core.TaskOpen), "created": true, "plan_slug": slug, "by": "console",
		})
	}
	if wantsJSON(r) {
		out := map[string]any{"slug": slug, "status": plans.StatusApproved, "taskCreated": created}
		if created {
			out["taskId"] = task.ID
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	http.Redirect(w, r, "/console/plans/"+slug, http.StatusSeeOther)
}

// planComposition resolves a plan slug to its plan note and the other notes
// tagged into the composition (agent caches and supporting notes). ok is false
// when no cc-plan note carries the tag.
func (s *Service) planComposition(ctx context.Context, slug string) (core.Note, []planNoteRef, bool, error) {
	tagged, err := store.NotesByTag(ctx, s.cfg.DB, "", plans.SlugTag(slug))
	if err != nil {
		return core.Note{}, nil, false, err
	}
	var planNote core.Note
	found := false
	var attached []planNoteRef
	for _, n := range tagged {
		if !found && slices.Contains(n.Tags, plans.TagPlan) {
			planNote = n
			found = true
			continue
		}
		attached = append(attached, planNoteRef{
			ID: n.ID, Title: n.Title, Slug: n.Slug,
			IsAgent: slices.Contains(n.Tags, plans.TagAgent), Updated: n.Updated,
		})
	}
	return planNote, attached, found, nil
}

// planAgentCounts counts agent-cache notes per plan slug, one query for the
// whole list.
func (s *Service) planAgentCounts(ctx context.Context) (map[string]int, error) {
	agents, err := store.NotesByTag(ctx, s.cfg.DB, "", plans.TagAgent)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(agents))
	for _, a := range agents {
		if slug := plans.SlugFromTags(a.Tags); slug != "" {
			counts[slug]++
		}
	}
	return counts, nil
}

// planRow projects a cc-plan index note into a display row. The iteration
// comes from the note file's frontmatter (best-effort; 0 when unreadable).
func (s *Service) planRow(ctx context.Context, n core.Note, agentCount map[string]int) planRow {
	slug := plans.SlugFromTags(n.Tags)
	row := planRow{
		NoteID: n.ID, Slug: slug, Basename: plans.Basename(n.Slug),
		Title: n.Title, Project: n.Project,
		Status: plans.StatusFromTags(n.Tags), Agents: agentCount[slug],
		Updated: n.Updated,
	}
	if s.cfg.Files != nil {
		if full, err := s.cfg.Files.Store().ReadNote(n.FilePath); err == nil {
			row.Iteration = plans.NoteIteration(full)
		}
	}
	if tasks, err := store.ListTasksForPlan(ctx, s.cfg.DB, n.Project, "", slug); err == nil {
		row.TasksTotal = len(tasks)
		for _, t := range tasks {
			if t.Status == core.TaskDone {
				row.TasksDone++
			}
		}
	}
	return row
}

// recordPlanAction appends a console-attributed plan event (best-effort).
func (s *Service) recordPlanAction(ctx context.Context, kind core.EventKind, project, itemID string, payload map[string]any) {
	if s.cfg.Events == nil {
		return
	}
	if _, err := s.cfg.Events.Record(ctx, core.Event{
		Kind: kind, ProjectSlug: project, ItemID: itemID, Payload: payload,
	}); err != nil {
		s.logger.Warn("console: record plan action", "kind", kind, "error", err)
	}
}

// planTone maps a plan status to a badge tone: approved green, presented
// brand-ish accent, draft amber, abandoned neutral.
func planTone(status string) string {
	switch status {
	case plans.StatusApproved:
		return "ok"
	case plans.StatusPresented:
		return "accent"
	case plans.StatusDraft:
		return "warn"
	default: // abandoned
		return ""
	}
}
