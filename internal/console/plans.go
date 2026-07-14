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
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/store"
)

// Plan sources: a "capture" row is a Claude Code plan-mode capture (cc-plan
// note, with an iteration/agents/basename); a "composed" row is a plain
// plans-as-composition plan (a note tagged plan:<slug> plus its tasks), which
// has none of the capture-only columns.
const (
	planSourceCapture  = "capture"
	planSourceComposed = "composed"
)

// Plan phases bucket a plan by the progress of its step tasks, driving the
// grouped list: in progress (a step is being worked), ready (open steps remain,
// or the plan has not started), done (all steps closed, or an abandoned
// capture).
const (
	planPhaseInProgress = "in_progress"
	planPhaseReady      = "ready"
	planPhaseDone       = "done"
)

// planRow is a display projection of one plan (capture or composed).
type planRow struct {
	NoteID     string    `json:"noteId"`
	Slug       string    `json:"slug"`     // plan:<slug> composition key
	Source     string    `json:"source"`   // capture | composed
	Basename   string    `json:"basename"` // CC plan file name without .md (captures only)
	Title      string    `json:"title"`
	Project    string    `json:"project,omitempty"`
	Status     string    `json:"status"`
	Iteration  int       `json:"iteration,omitempty"`
	Agents     int       `json:"agents"` // cached subagent notes in the composition
	TasksDone  int       `json:"tasksDone"`
	TasksOpen  int       `json:"tasksOpen"`       // open (not started, not closed) steps
	TasksWIP   int       `json:"tasksInProgress"` // steps currently being worked
	TasksTotal int       `json:"tasksTotal"`
	Phase      string    `json:"phase"` // in_progress | ready | done
	Updated    time.Time `json:"updated"`
}

// plansData is the Plans list payload. Rows are one merged, newest-first list;
// the phase counts drive the three grouped sections the template renders (in
// progress, then ready, then done).
type plansData struct {
	Rows        []planRow      `json:"rows"`
	Count       int            `json:"count"`
	InProgress  int            `json:"inProgress"`
	Ready       int            `json:"ready"`
	Done        int            `json:"done"`
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
	agentCount, err := s.planAgentCounts(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Captures are the primary of their slug. A cc-plan note also carries its
	// plan:<slug> tag, so track which (project, slug) pairs a capture already owns
	// and let composed plans fill only the rest.
	captures, err := store.NotesByTag(ctx, s.cfg.DB, "", plans.TagPlan)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rows := make([]planRow, 0, len(captures))
	owned := make(map[string]bool, len(captures))
	for _, n := range captures {
		row := s.planRow(ctx, n, agentCount)
		owned[n.Project+"\x00"+row.Slug] = true
		rows = append(rows, row)
	}
	// Composed plans: any note carrying a plan:<slug> tag that is not itself a
	// capture. The earliest-created non-agent note is the narrative primary.
	composed, err := store.NotesByTagPrefix(ctx, s.cfg.DB, "", plans.SlugTagPrefix())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, n := range composedPrimaries(composed, owned) {
		rows = append(rows, s.planRow(ctx, n, agentCount))
	}
	// One merged ordering across both sources; window-filter last so it applies
	// uniformly.
	kept := rows[:0]
	for _, row := range rows {
		if win.Since.IsZero() || !row.Updated.Before(win.Since) {
			kept = append(kept, row)
		}
	}
	slices.SortFunc(kept, func(a, b planRow) int {
		if !a.Updated.Equal(b.Updated) {
			return b.Updated.Compare(a.Updated)
		}
		return strings.Compare(b.NoteID, a.NoteID)
	})
	data := plansData{
		Rows: kept, Count: len(kept),
		Window: win.Key, WindowLabel: win.Label, Windows: windowOptions(win.Key),
	}
	for _, row := range kept {
		switch row.Phase {
		case planPhaseInProgress:
			data.InProgress++
		case planPhaseDone:
			data.Done++
		default:
			data.Ready++
		}
	}
	s.render(w, r, "plans", pageData{Title: "Plans", Active: "plans", Data: data})
}

// composedPrimaries groups plan:<slug> notes by (project, slug) and returns the
// narrative primary of each composed plan: the earliest-created note that is
// neither an agent cache nor already owned by a capture. Groups with only agent
// caches, or whose slug a capture already represents, are skipped.
func composedPrimaries(notes []core.Note, owned map[string]bool) []core.Note {
	primary := make(map[string]core.Note)
	for _, n := range notes {
		if slices.Contains(n.Tags, plans.TagPlan) || slices.Contains(n.Tags, plans.TagAgent) {
			continue
		}
		slug := plans.SlugFromTags(n.Tags)
		if slug == "" {
			continue
		}
		key := n.Project + "\x00" + slug
		if owned[key] {
			continue
		}
		if cur, ok := primary[key]; !ok || earlierPrimary(n, cur) {
			primary[key] = n
		}
	}
	out := make([]core.Note, 0, len(primary))
	for _, n := range primary {
		out = append(out, n)
	}
	return out
}

// earlierPrimary reports whether a should win over b as a plan's narrative
// primary: the earlier Created wins, ties broken by the lower id for stability.
func earlierPrimary(a, b core.Note) bool {
	if !a.Created.Equal(b.Created) {
		return a.Created.Before(b.Created)
	}
	return a.ID < b.ID
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
		s.notFound(w, r, "No plan with slug "+slug+".")
		return
	}
	agentCount, err := s.planAgentCounts(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := planDetailData{Row: s.planRow(ctx, planNote, agentCount), Attached: attached}
	// The approve escape hatch is a CC-capture lifecycle action; composed plans
	// have no draft/presented/approved state to flip.
	d.CanApprove = d.Row.Source == planSourceCapture && d.Row.Status != plans.StatusApproved
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
	// renderDetail serves this three ways: JSON (CLI), the peek fragment
	// (?peek=1, for the detail pane plans rows open), or -- by default -- the
	// bespoke full plan.html page (since "plan" is a registered page).
	s.renderDetail(w, r, "plan", pageData{Title: "Plan " + d.Row.Title, Active: "plans", Data: d})
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
		s.notFound(w, r, "No plan with slug "+slug+".")
		return
	}
	// The escape hatch only means anything for a CC capture; a composed plan has
	// no capture lifecycle to flip.
	if !slices.Contains(planNote.Tags, plans.TagPlan) {
		s.notFound(w, r, "Plan "+slug+" is not a Claude Code capture; nothing to approve.")
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

// planComposition resolves a plan slug to its primary note and the other notes
// tagged into the composition (agent caches and supporting notes). The primary
// is the cc-plan capture when one carries the tag; otherwise it is the composed
// plan's narrative -- the earliest-created non-agent note. ok is false when no
// note carries the tag at all.
func (s *Service) planComposition(ctx context.Context, slug string) (core.Note, []planNoteRef, bool, error) {
	tagged, err := store.NotesByTag(ctx, s.cfg.DB, "", plans.SlugTag(slug))
	if err != nil {
		return core.Note{}, nil, false, err
	}
	primary := -1
	for i, n := range tagged {
		if slices.Contains(n.Tags, plans.TagPlan) {
			primary = i
			break
		}
	}
	if primary == -1 {
		for i, n := range tagged {
			if slices.Contains(n.Tags, plans.TagAgent) {
				continue
			}
			if primary == -1 || earlierPrimary(n, tagged[primary]) {
				primary = i
			}
		}
	}
	if primary == -1 {
		return core.Note{}, nil, false, nil
	}
	var attached []planNoteRef
	for i, n := range tagged {
		if i == primary {
			continue
		}
		attached = append(attached, planNoteRef{
			ID: n.ID, Title: n.Title, Slug: n.Slug,
			IsAgent: slices.Contains(n.Tags, plans.TagAgent), Updated: n.Updated,
		})
	}
	return tagged[primary], attached, true, nil
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

// planRow projects a plan's primary note into a display row. The capture-only
// columns (basename, iteration) are read from the cc-plan note's frontmatter
// (best-effort); composed primaries carry neither.
func (s *Service) planRow(ctx context.Context, n core.Note, agentCount map[string]int) planRow {
	slug := plans.SlugFromTags(n.Tags)
	isCapture := slices.Contains(n.Tags, plans.TagPlan)
	row := planRow{
		NoteID: n.ID, Slug: slug, Source: planSourceComposed,
		Title: n.Title, Project: n.Project,
		Status: plans.StatusFromTags(n.Tags), Agents: agentCount[slug],
		Updated: n.Updated,
	}
	if isCapture {
		row.Source = planSourceCapture
		row.Basename = plans.Basename(n.Slug)
		if s.cfg.Files != nil {
			if full, err := s.cfg.Files.Store().ReadNote(n.FilePath); err == nil {
				row.Iteration = plans.NoteIteration(full)
			}
		}
	}
	if tasks, err := store.ListTasksForPlan(ctx, s.cfg.DB, n.Project, "", slug); err == nil {
		row.TasksTotal = len(tasks)
		for _, t := range tasks {
			switch t.Status {
			case core.TaskDone:
				row.TasksDone++
			case core.TaskInProgress:
				row.TasksWIP++
			case core.TaskOpen:
				row.TasksOpen++
			}
		}
	}
	row.Phase = planPhase(row)
	return row
}

// planPhase buckets a plan by the progress of its step tasks. A step in progress
// wins outright (matching "has an in-progress task"). Otherwise the plan is done
// when it is terminal -- an abandoned capture, or every step closed with none
// open -- and ready when open steps remain or it has not started yet.
func planPhase(r planRow) string {
	switch {
	case r.TasksWIP > 0:
		return planPhaseInProgress
	case r.Status == plans.StatusAbandoned:
		return planPhaseDone
	case r.TasksTotal > 0 && r.TasksOpen == 0:
		return planPhaseDone
	default:
		return planPhaseReady
	}
}

// phaseRows filters a newest-first plan list to one phase, preserving order, so
// the template can render each grouped section from the single merged slice.
func phaseRows(rows []planRow, phase string) []planRow {
	out := make([]planRow, 0, len(rows))
	for _, r := range rows {
		if r.Phase == phase {
			out = append(out, r)
		}
	}
	return out
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
