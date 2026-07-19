package console

// The project-detail workspace's tab panels. Each fillXTab loads exactly one
// panel into projectWorkspaceData and owns that tab's view models; the page
// assembly lives in project_detail.go.

import (
	"context"
	"encoding/json"
	"html/template"
	"sort"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/markdown"
	"github.com/0spoon/seamless/internal/store"
)

// planTimelineVM is one plan's step timeline on the Plans & tasks tab.
type planTimelineVM struct {
	Slug     string
	Total    int
	Done     int
	InFlight int
	Open     int
	DonePct  int
	DoingPct int
	Steps    []planStepVM
}

// planStepVM is one step in a plan timeline: its status, claim/lease, and the
// blocking dependency (if any). State is the CSS row modifier (done/doing/
// blocked/open); it is derived, never stored.
type planStepVM struct {
	ID          string
	Title       string
	Status      string
	State       string
	ClaimedBy   string // session name
	ClaimedByID string
	LeaseLeft   string
	BlockedBy   string
}

// projSessionVM is one row on the Sessions tab.
type projSessionVM struct {
	ID       string
	Name     string
	Status   string
	Source   string
	Ambient  bool
	Active   bool
	Findings string
	Holds    []string // labels of tasks the session currently claims
	Updated  time.Time
}

// projMemoryVM is one row on the Memories tab, carrying a rendered lineage cell
// (provenance session, or a supersession pointer).
type projMemoryVM struct {
	ID         string
	Name       string
	Kind       string
	Desc       string
	Lineage    template.HTML
	Injects    int
	Reads      int
	Updated    time.Time
	Superseded bool
}

// projNoteVM is one row on the Notes tab.
type projNoteVM struct {
	ID      string
	Title   string
	Desc    string
	Tags    []string
	Updated time.Time
}

// fillOverviewTab loads the Overview panel: memories-by-kind (project's active
// memories), this project's injection trend (its own memories, matching the
// board reach), and the project's recent activity.
func (s *Service) fillOverviewTab(ctx context.Context, data *projectWorkspaceData, slug string, win store.RetrievalWindow) error {
	active, err := store.ActiveMemories(ctx, s.cfg.DB, slug)
	if err != nil {
		return err
	}
	byKind := map[string]int{}
	for _, m := range active {
		if m.Project == slug { // strict: exclude inherited global/parent rows
			byKind[string(m.Kind)]++
		}
	}
	data.MemByKind = orderKinds(byKind)

	trend, err := store.ProjectRetrievalTrend(ctx, s.cfg.DB, win, slug)
	if err != nil {
		return err
	}
	data.Trend = trend

	if s.cfg.Events != nil {
		evs, err := s.cfg.Events.RecentExcluding(ctx, 120, core.EventToolCall, core.EventHookPrompt)
		if err != nil {
			return err
		}
		for _, e := range evs {
			if e.ProjectSlug != slug {
				continue
			}
			data.Recent = append(data.Recent, toEventRow(e))
			if len(data.Recent) >= 8 {
				break
			}
		}
	}
	return nil
}

// fillTasksTab loads the Plans & tasks panel: each active plan's step timeline
// (claim/lease/blocked) and the off-plan ready queue.
func (s *Service) fillTasksTab(ctx context.Context, data *projectWorkspaceData, slug string) error {
	plans, err := store.ActivePlans(ctx, s.cfg.DB, slug)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	statusCache := map[string]core.Task{}
	for _, pl := range plans {
		steps, err := store.ListTasksForPlan(ctx, s.cfg.DB, slug, "", pl.Slug)
		if err != nil {
			return err
		}
		for _, st := range steps {
			statusCache[st.ID] = st
		}
		tl := planTimelineVM{
			Slug: pl.Slug, Total: pl.Total, Done: pl.Done, InFlight: pl.InFlight,
			Open:    pl.Total - pl.Done - pl.InFlight,
			DonePct: percent(pl.Done, pl.Total), DoingPct: percent(pl.InFlight, pl.Total),
		}
		for _, st := range steps {
			tl.Steps = append(tl.Steps, s.planStep(ctx, st, statusCache, now))
		}
		data.Plans = append(data.Plans, tl)
	}

	ready, err := store.ReadyTasks(ctx, s.cfg.DB, slug)
	if err != nil {
		return err
	}
	data.Ready = taskRows(ready)
	return nil
}

// planStep projects a plan-step task into a timeline row, resolving its claim
// (session name + remaining lease) and its blocking dependency (if open).
func (s *Service) planStep(ctx context.Context, t core.Task, cache map[string]core.Task, now time.Time) planStepVM {
	step := planStepVM{ID: t.ID, Title: t.Title, Status: string(t.Status)}
	switch {
	case t.Status == core.TaskDone || t.Status == core.TaskDropped:
		step.State = "done"
	case t.Status == core.TaskInProgress:
		step.State = "doing"
	default:
		step.State = "open"
	}
	if t.ClaimLive(now) {
		if sess, ok, err := store.SessionByID(ctx, s.cfg.DB, t.ClaimedBy); err == nil && ok {
			step.ClaimedBy, step.ClaimedByID = sess.Name, sess.ID
		}
		if t.LeaseExpiresAt != nil {
			step.LeaseLeft = durUntil(*t.LeaseExpiresAt, now)
		}
	}
	if t.Status == core.TaskOpen {
		if blocker, blocked := s.blockingDep(ctx, t, cache); blocked {
			step.State = "blocked"
			step.BlockedBy = blocker
		}
	}
	return step
}

// fillSessionsTab loads the Sessions panel: the project's sessions newest first,
// with each active session's held tasks resolved for an inline claim indicator.
func (s *Service) fillSessionsTab(ctx context.Context, data *projectWorkspaceData, slug string) error {
	sessions, err := store.ListSessionsForProject(ctx, s.cfg.DB, slug, "", time.Time{}, 60)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, sess := range sessions {
		vm := projSessionVM{
			ID: sess.ID, Name: sess.Name, Status: string(sess.Status),
			Source: sess.Source, Ambient: sess.Ambient,
			Active:   sess.Status == core.SessionActive,
			Findings: snippet(markdown.PlainText(sess.Findings), 140), Updated: sess.UpdatedAt,
		}
		if vm.Active {
			if held, err := store.TasksClaimedBy(ctx, s.cfg.DB, sess.ID); err == nil {
				for _, t := range held {
					if t.ClaimLive(now) {
						vm.Holds = append(vm.Holds, taskLabel(t))
					}
				}
			}
		}
		data.Sessions = append(data.Sessions, vm)
	}
	return nil
}

// fillMemoriesTab loads the Memories panel: the project's strict memories
// (active + superseded, with lineage) and, as a separate labeled group, the
// global/parent memories it inherits -- so the tab count reconciles with the
// board's strict per-slug count instead of silently disagreeing.
func (s *Service) fillMemoriesTab(ctx context.Context, data *projectWorkspaceData, slug string) error {
	strict, err := store.ProjectMemoriesIncludingInvalid(ctx, s.cfg.DB, slug)
	if err != nil {
		return err
	}
	for _, m := range strict {
		data.Memories = append(data.Memories, s.projMemory(ctx, m))
	}
	active, err := store.ActiveMemories(ctx, s.cfg.DB, slug)
	if err != nil {
		return err
	}
	for _, m := range active {
		if m.Project == slug {
			continue // strict rows already shown above
		}
		data.Inherited = append(data.Inherited, s.projMemory(ctx, m))
	}
	return nil
}

// projMemory projects a memory into a Memories-tab row, building its lineage
// cell and joining its retrieval stats.
func (s *Service) projMemory(ctx context.Context, m core.Memory) projMemoryVM {
	vm := projMemoryVM{
		ID: m.ID, Name: m.Name, Kind: string(m.Kind), Desc: m.Description,
		Updated: m.Updated, Superseded: !m.Active(),
		Lineage: s.memLineage(ctx, m),
	}
	if stat, ok, err := store.GetRetrievalStat(ctx, s.cfg.DB, m.ID); err == nil && ok {
		vm.Injects, vm.Reads = stat.InjectCount, stat.ReadCount
	}
	return vm
}

// memLineage renders a memory's provenance cell: a supersession pointer when the
// memory was replaced, else its source session, else nothing.
func (s *Service) memLineage(ctx context.Context, m core.Memory) template.HTML {
	if m.SupersededBy != "" {
		name := m.SupersededBy
		if by, ok, err := store.MemoryByID(ctx, s.cfg.DB, m.SupersededBy); err == nil && ok {
			name = by.Name
		}
		return template.HTML(`<span class="lineage" style="color:var(--pop-strong)">` +
			string(icon("arrow-right")) + `superseded by ` + template.HTMLEscapeString(name) + `</span>`)
	}
	if m.SourceSession != "" {
		return template.HTML(`<span class="lineage">` + string(icon("git-commit-horizontal")) +
			`from ` + template.HTMLEscapeString(m.SourceSession) + `</span>`)
	}
	return template.HTML(`<span class="lineage faint">&mdash;</span>`)
}

// fillNotesTab loads the Notes panel: the project's notes (a Go-side filter of
// the note index, newest first).
func (s *Service) fillNotesTab(ctx context.Context, data *projectWorkspaceData, slug string) error {
	notes, err := store.ListNotes(ctx, s.cfg.DB)
	if err != nil {
		return err
	}
	for _, n := range notes {
		if n.Project != slug {
			continue
		}
		data.Notes = append(data.Notes, projNoteVM{
			ID: n.ID, Title: n.Title, Desc: n.Description, Tags: n.Tags, Updated: n.Updated,
		})
	}
	sort.SliceStable(data.Notes, func(i, j int) bool {
		return data.Notes[i].Updated.After(data.Notes[j].Updated)
	})
	return nil
}

// fillInteractionsTab loads the Interactions panel: the transport feed filtered
// to this project's events (interactionRow already carries the project slug), so
// no new store query is needed.
func (s *Service) fillInteractionsTab(ctx context.Context, data *projectWorkspaceData, slug string) error {
	if s.cfg.Events == nil {
		return nil
	}
	evs, err := s.cfg.Events.ByKinds(ctx, interactionKinds, "", "", interactionsPageLimit)
	if err != nil {
		return err
	}
	name := s.sessionNamer(ctx)
	for _, e := range evs {
		if e.ProjectSlug != slug || skipInteraction(e) {
			continue
		}
		data.Interactions = append(data.Interactions, toInteractionRow(e, name))
		if len(data.Interactions) >= 40 {
			break
		}
	}
	// Project-scoped volume histogram over all of the project's history; embedded
	// as compact JSON for the shared IX.renderVolume client renderer.
	if vol, err := s.interactionVolume(ctx, slug, 0); err != nil {
		return err
	} else if len(vol) > 0 {
		if b, err := json.Marshal(vol); err == nil {
			data.IxVolumeJSON = string(b)
		}
	}
	return nil
}

// taskLabel is a short human label for a claimed task: its plan-step marker when
// it has a plan, else a truncated id.
func taskLabel(t core.Task) string {
	if t.PlanSlug != "" {
		return t.PlanSlug + " step"
	}
	return shortID(t.ID)
}
