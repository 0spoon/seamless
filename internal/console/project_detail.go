package console

// The project-detail workspace turns the thin project peek (a summary drawer)
// into a full tabbed page: the peek drawer stays the at-a-glance summary, while
// the full page (the "open" target) is a 7-tab workspace over the project's
// health, plans/tasks, sessions, memories, notes, interactions, and relations.
// The relations tree + cross-project banners are built by helpers here and
// reused by the standalone /console/relations screen.

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/markdown"
	"github.com/0spoon/seamless/internal/store"
)

// projectTabKeys are the workspace tabs in bar order. A ?tab= deep-link outside
// this set is a 400 (never a silent fallback to Overview), matching the board's
// strict-param policy so an agent driving the console by URL sees the fix.
var projectTabKeys = []string{"overview", "tasks", "sessions", "memories", "notes", "interactions", "relations"}

// projectWorkspaceData is the full project-detail page payload: the header, the
// tab bar (with counts), and every tab's server-rendered panel.
type projectWorkspaceData struct {
	Slug        string
	Name        string
	Description string
	TileClass   string
	TileIcon    string
	Live        int  // active sessions ("N agents working")
	IsRoot      bool // no parent (a root project injects only its own memories)
	Parent      string
	Retired     bool
	ActiveTab   string
	Tabs        []projectTabVM

	Metrics   projectMetrics
	Trend     []store.TrendBucket // global injection trend (no per-project series exists yet)
	MemByKind []kindCount
	Recent    []eventRow

	Plans   []planTimelineVM
	Ready   []taskRow
	Blocked int // open tasks blocked by an unfinished dependency

	Sessions []projSessionVM

	Memories  []projMemoryVM
	Inherited []projMemoryVM // global/parent memories a strict project count excludes

	Notes []projNoteVM

	Interactions []interactionRow

	Tree    []treeNode
	Banners []relBanner
}

// projectTabVM is one entry in the workspace tab bar.
type projectTabVM struct {
	Key      string
	Label    string
	Icon     string
	Count    int
	HasCount bool
	Active   bool
}

// projectMetrics is the workspace header metric bar: strict per-slug health
// (matching the board row) plus windowed coverage.
type projectMetrics struct {
	Memories    int
	Sessions    int
	Live        int
	OpenTasks   int
	Blocked     int
	HasReach    bool
	ReachRate   int
	HasCoverage bool
	Coverage    int
}

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

// treeNode is one node of the relations tree (plan -> step -> claiming session
// -> memory). Cap is server-built HTML (status chips, edges); Kids nest under it.
type treeNode struct {
	Lead string // plan|task|sess|mem -> CSS lead-<x>
	Icon string
	Name string
	Href string
	Cap  template.HTML
	Flag bool // an active/live session (adds .tflag)
	Kids []treeNode
}

// relBanner is one "cross-project ties" banner: a shared-briefing note or a
// retired-by-split lineage note. HTML is server-built (bolded project names).
type relBanner struct {
	Icon    string
	Retired bool
	HTML    template.HTML
}

// projectDetail dispatches the /console/projects/{slug} route: the peek drawer
// (?peek=1) and the CLI (JSON) get the thin summary; a browser hitting the full
// page gets the tabbed workspace. A retired project still renders (with its
// banner), only an unknown slug 404s.
func (s *Service) projectDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := r.PathValue("slug")
	p, ok, err := store.ProjectBySlug(ctx, s.cfg.DB, slug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("peek") == "1" || wantsJSON(r) {
		s.projectSummary(w, r, p)
		return
	}
	s.projectWorkspace(w, r, p)
}

// projectSummary renders the thin project peek: metadata + per-channel counts,
// served as the drawer fragment (?peek=1) or CLI JSON.
func (s *Service) projectSummary(w http.ResponseWriter, r *http.Request, p core.Project) {
	ctx := r.Context()
	counts, err := store.GetProjectCounts(ctx, s.cfg.DB, p.Slug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := projectDetail{
		Slug: p.Slug, Name: p.Name, Description: p.Description,
		Memories: counts.Memories, Sessions: counts.Sessions,
		OpenTasks: counts.OpenTasks, Notes: counts.Notes,
		Created: p.CreatedAt, Updated: p.UpdatedAt,
	}
	s.renderDetail(w, r, "project", pageData{Title: "Project " + p.Slug, Active: "projects", Data: d})
}

// projectWorkspace builds and renders the full tabbed workspace page.
func (s *Service) projectWorkspace(w http.ResponseWriter, r *http.Request, p core.Project) {
	ctx := r.Context()
	slug := p.Slug

	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "overview"
	}
	if !slices.Contains(projectTabKeys, tab) {
		s.badRequest(w, r, fmt.Sprintf("invalid tab %q: valid values are %s",
			tab, strings.Join(projectTabKeys, ", ")))
		return
	}
	win := store.ResolveRetrievalWindow(r.URL.Query().Get("w"), time.Now())

	data := projectWorkspaceData{
		Slug: slug, Name: p.Name, Description: p.Description,
		Parent: p.ParentSlug, Retired: p.Retired(), ActiveTab: tab,
		IsRoot: p.ParentSlug == "",
	}
	if data.Name == "" {
		data.Name = slug
	}
	data.TileClass, data.TileIcon = "grp", "server"

	// Header + metric bar: strict per-slug health from the same batched query the
	// board uses, so a single row equals the board row exactly.
	board, err := store.ProjectsWithCounts(ctx, s.cfg.DB, win)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var row store.ProjectBoardRow
	for _, b := range board {
		if b.Project == slug {
			row = b
			break
		}
	}
	data.Live = row.LiveSessions
	data.Metrics = projectMetrics{
		Memories: row.Memories, Sessions: row.Sessions, Live: row.LiveSessions,
		OpenTasks: row.OpenTasks, Blocked: row.Blocked,
		HasReach: row.Active > 0, ReachRate: row.ReachRate,
	}
	data.Blocked = row.Blocked
	if cov, cerr := store.GetSessionCoverageForProject(ctx, s.cfg.DB, slug, win.Since); cerr == nil && cov.Total > 0 {
		data.Metrics.HasCoverage = true
		data.Metrics.Coverage = percent(cov.Covered, cov.Total)
	}

	if err := s.fillOverviewTab(ctx, &data, slug, win); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.fillTasksTab(ctx, &data, slug); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.fillSessionsTab(ctx, &data, slug); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.fillMemoriesTab(ctx, &data, slug); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.fillNotesTab(ctx, &data, slug); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.fillInteractionsTab(ctx, &data, slug); err != nil {
		s.serverError(w, r, err)
		return
	}
	tree, err := s.buildProjectTree(ctx, slug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data.Tree = tree
	banners, err := s.projectBanners(ctx, p)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data.Banners = banners

	data.Tabs = []projectTabVM{
		{Key: "overview", Label: "Overview", Icon: "gauge"},
		{Key: "tasks", Label: "Plans & tasks", Icon: "list-checks", Count: data.Metrics.OpenTasks, HasCount: true},
		{Key: "sessions", Label: "Sessions", Icon: "terminal", Count: data.Metrics.Sessions, HasCount: true},
		{Key: "memories", Label: "Memories", Icon: "brain", Count: data.Metrics.Memories, HasCount: true},
		{Key: "notes", Label: "Notes", Icon: "file-text", Count: len(data.Notes), HasCount: true},
		{Key: "interactions", Label: "Interactions", Icon: "activity"},
		{Key: "relations", Label: "Relations", Icon: "share-2"},
	}
	for i := range data.Tabs {
		data.Tabs[i].Active = data.Tabs[i].Key == tab
	}

	s.render(w, r, "projectdetail", pageData{Title: "Project " + slug, Active: "projects", Data: data})
}

// fillOverviewTab loads the Overview panel: memories-by-kind (project's active
// memories), the global injection trend (no per-project series exists), and the
// project's recent activity.
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

	report, err := store.BuildRetrievalReport(ctx, s.cfg.DB, win, 0)
	if err != nil {
		return err
	}
	data.Trend = report.Trend

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
		for _, depID := range t.DependsOn {
			dep, ok := cache[depID]
			if !ok {
				d, err := store.TaskByID(ctx, s.cfg.DB, depID)
				if err != nil {
					continue
				}
				dep, cache[depID] = d, d
			}
			if dep.Status == core.TaskOpen || dep.Status == core.TaskInProgress {
				step.State = "blocked"
				step.BlockedBy = dep.Title
				break
			}
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
	return nil
}

// buildProjectTree assembles the relations tree for a project: each plan (active
// or completed) expands into its steps, each step into its claiming session, and
// each session into the memories it produced. Shared with the /console/relations
// screen.
func (s *Service) buildProjectTree(ctx context.Context, project string) ([]treeNode, error) {
	slugs, err := store.DistinctPlanSlugsForProject(ctx, s.cfg.DB, project)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	cache := map[string]core.Task{} // task IDs are global, so one cache spans plans
	var out []treeNode
	for _, plan := range slugs {
		steps, err := store.ListTasksForPlan(ctx, s.cfg.DB, project, "", plan)
		if err != nil {
			return nil, err
		}
		for _, st := range steps {
			cache[st.ID] = st
		}
		done := 0
		for _, st := range steps {
			if st.Status == core.TaskDone || st.Status == core.TaskDropped {
				done++
			}
		}
		node := treeNode{
			Lead: "plan", Icon: "git-merge", Name: "plan:" + plan,
			Cap: template.HTML(fmt.Sprintf(`&middot; %d steps &middot; %d%% done`,
				len(steps), percent(done, len(steps)))),
		}
		for _, st := range steps {
			node.Kids = append(node.Kids, s.treeStep(ctx, st, cache, now))
		}
		out = append(out, node)
	}
	return out, nil
}

// treeStep builds a step node and its claiming-session / memory subtree. An open
// step whose dependency is still unfinished carries a "blocked by" edge in its
// caption, so the tree reads the dependency spine like the mock's relations view.
func (s *Service) treeStep(ctx context.Context, t core.Task, cache map[string]core.Task, now time.Time) treeNode {
	step := treeNode{Lead: "task", Icon: taskTreeIcon(string(t.Status)), Name: t.Title}
	capHTML := kindChip(string(t.Status), taskTone(string(t.Status)))
	if t.Status == core.TaskOpen {
		if blocker := s.blockingDep(ctx, t, cache); blocker != "" {
			capHTML += ` <span class="edge" style="padding:0 6px">&larr; blocked by ` +
				template.HTMLEscapeString(blocker) + `</span>`
		}
	}
	step.Cap = template.HTML(capHTML)
	if t.ClaimLive(now) {
		if sess, ok, err := store.SessionByID(ctx, s.cfg.DB, t.ClaimedBy); err == nil && ok {
			step.Kids = append(step.Kids, s.treeSession(ctx, sess))
		}
	}
	return step
}

// blockingDep returns the title of the first unfinished dependency (open or
// in-progress) of an open step, or "" if nothing blocks it. It mirrors
// planStep's dependency walk and caches resolved tasks across the whole tree.
func (s *Service) blockingDep(ctx context.Context, t core.Task, cache map[string]core.Task) string {
	for _, depID := range t.DependsOn {
		dep, ok := cache[depID]
		if !ok {
			d, err := store.TaskByID(ctx, s.cfg.DB, depID)
			if err != nil {
				continue
			}
			dep, cache[depID] = d, d
		}
		if dep.Status == core.TaskOpen || dep.Status == core.TaskInProgress {
			return dep.Title
		}
	}
	return ""
}

// treeSession builds a claiming-session node and its produced-memory leaves.
func (s *Service) treeSession(ctx context.Context, sess core.Session) treeNode {
	node := treeNode{
		Lead: "sess", Icon: "terminal", Name: sess.Name,
		Href: "/console/sessions/" + sess.ID,
	}
	if sess.Status == core.SessionActive {
		node.Flag = true
		node.Cap = template.HTML(`<span class="live-dot"></span>active`)
	} else {
		node.Cap = template.HTML(template.HTMLEscapeString(sess.Source) + " &middot; closed " + ago(sess.UpdatedAt))
	}
	mems, err := store.MemoriesForSession(ctx, s.cfg.DB, sess.Name)
	if err != nil {
		return node
	}
	for _, m := range mems {
		leaf := treeNode{
			Lead: "mem", Icon: "brain", Name: m.Name,
			Href: "/console/memories/" + m.ID,
			Cap:  template.HTML(kindChip(string(m.Kind), "")),
		}
		node.Kids = append(node.Kids, leaf)
	}
	return node
}

// projectBanners builds the cross-project ties for one project's relations view:
// the shared-briefing banner (root / parent / child) plus, when the project was
// retired by a split, its lineage banner.
func (s *Service) projectBanners(ctx context.Context, p core.Project) ([]relBanner, error) {
	var banners []relBanner
	children, err := store.ProjectsByParent(ctx, s.cfg.DB, p.Slug)
	if err != nil {
		return nil, err
	}
	switch {
	case p.Retired():
		// handled by the lineage banner below
	case p.ParentSlug != "":
		parentMem, err := store.GetProjectCounts(ctx, s.cfg.DB, p.ParentSlug)
		if err != nil {
			return nil, err
		}
		banners = append(banners, relBanner{Icon: "git-branch", HTML: template.HTML(fmt.Sprintf(
			`<b>%s</b> inherits %s from its parent <b>%s</b> at briefing time.`,
			template.HTMLEscapeString(p.Slug), plural(parentMem.Memories, "active memory", "active memories"),
			template.HTMLEscapeString(p.ParentSlug)))})
	case len(children) > 0:
		mem, err := store.GetProjectCounts(ctx, s.cfg.DB, p.Slug)
		if err != nil {
			return nil, err
		}
		banners = append(banners, relBanner{Icon: "git-branch", HTML: template.HTML(fmt.Sprintf(
			`<b>%s</b> shares %s into %s at briefing time &mdash; children inherit without duplicating.`,
			template.HTMLEscapeString(p.Slug), plural(mem.Memories, "active memory", "active memories"),
			childList(children)))})
	default:
		banners = append(banners, relBanner{Icon: "git-branch", HTML: template.HTML(fmt.Sprintf(
			`As a root project, <b>%s</b> injects only its own memories &mdash; no parent to inherit from and no children.`,
			template.HTMLEscapeString(p.Slug)))})
	}
	if p.Retired() {
		banner, err := s.splitLineageBanner(ctx, p)
		if err != nil {
			return nil, err
		}
		if banner != nil {
			banners = append(banners, *banner)
		}
	}
	return banners, nil
}

// splitLineageBanner builds the retired-by-split banner for a project: how long
// ago it was retired and where its memories moved (aggregated from the
// memory.moved event stream, from == the retired slug).
func (s *Service) splitLineageBanner(ctx context.Context, p core.Project) (*relBanner, error) {
	if s.cfg.Events == nil || p.RetiredAt == nil {
		return nil, nil
	}
	evs, err := s.cfg.Events.ByKinds(ctx, []core.EventKind{core.EventMemoryMoved}, "", "", 500)
	if err != nil {
		return nil, err
	}
	moved := 0
	targets := map[string]bool{}
	for _, e := range evs {
		if payloadStr(e.Payload, "from") != p.Slug {
			continue
		}
		moved++
		if to := payloadStr(e.Payload, "to"); to != "" {
			targets[to] = true
		}
	}
	dst := make([]string, 0, len(targets))
	for t := range targets {
		dst = append(dst, t)
	}
	sort.Strings(dst)
	days := int(p.RetiredAt.Sub(time.Time{}) / (24 * time.Hour))
	if !p.RetiredAt.IsZero() {
		days = int(time.Since(*p.RetiredAt).Hours() / 24)
	}
	var msg string
	if moved > 0 && len(dst) > 0 {
		msg = fmt.Sprintf(`<b>%s</b> was split %d days ago; its %s moved to %s. Kept readable for provenance.`,
			template.HTMLEscapeString(p.Slug), days, plural(moved, "memory", "memories"), boldList(dst))
	} else {
		msg = fmt.Sprintf(`<b>%s</b> was retired by a split %d days ago; kept readable for provenance.`,
			template.HTMLEscapeString(p.Slug), days)
	}
	return &relBanner{Icon: "split", Retired: true, HTML: template.HTML(msg)}, nil
}

// ---------------------------------------------------------------------------
// small formatting helpers
// ---------------------------------------------------------------------------

// taskTreeIcon maps a task status to its relations-tree glyph.
func taskTreeIcon(status string) string {
	switch status {
	case "done":
		return "check"
	case "in_progress":
		return "loader"
	case "dropped":
		return "circle"
	default:
		return "circle"
	}
}

// kindChip renders a compact status/kind chip for a tree caption.
func kindChip(text, tone string) string {
	cls := "kind"
	if tone != "" {
		cls += " " + tone
	}
	return `<span class="` + cls + `" style="padding:0 6px">` + template.HTMLEscapeString(text) + `</span>`
}

// taskLabel is a short human label for a claimed task: its plan-step marker when
// it has a plan, else a truncated id.
func taskLabel(t core.Task) string {
	if t.PlanSlug != "" {
		return t.PlanSlug + " step"
	}
	return shortID(t.ID)
}

// durUntil renders the remaining time until t as a compact "22m left" / "1h left".
func durUntil(t, now time.Time) string {
	d := t.Sub(now)
	if d <= 0 {
		return "expired"
	}
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm left", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh left", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd left", int(d.Hours()/24))
	}
}

// childList renders a project's children as a human "a, b and c" list, bolded.
func childList(children []core.Project) string {
	names := make([]string, 0, len(children))
	for _, c := range children {
		names = append(names, c.Slug)
	}
	return boldList(names)
}

// boldList renders slugs as a bolded, HTML-escaped "a, b and c" list.
func boldList(names []string) string {
	esc := make([]string, 0, len(names))
	for _, n := range names {
		esc = append(esc, "<b>"+template.HTMLEscapeString(n)+"</b>")
	}
	switch len(esc) {
	case 0:
		return ""
	case 1:
		return esc[0]
	case 2:
		return esc[0] + " and " + esc[1]
	default:
		return strings.Join(esc[:len(esc)-1], ", ") + ", and " + esc[len(esc)-1]
	}
}
