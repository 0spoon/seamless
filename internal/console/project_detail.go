package console

// The project-detail workspace turns the thin project peek into a full tabbed
// page: the peek fragment stays the at-a-glance summary, while the full page
// (the "open" target) is a 7-tab workspace over the project's health,
// plans/tasks, sessions, memories, notes, interactions, and relations.
//
// This file is the route dispatch and page assembly. The tab panels live in
// project_tabs.go; the relations tree and cross-project banners live in
// project_tree.go and project_banners.go, both shared with /console/relations.

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
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
	IxVolumeJSON string // compact JSON of the project's volume buckets, for IX.renderVolume

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

// projectDetail dispatches the /console/projects/{slug} route: the peek fragment
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
		s.notFound(w, r, "No project with slug "+slug+".")
		return
	}
	if r.URL.Query().Get("peek") == "1" || wantsJSON(r) {
		s.projectSummary(w, r, p)
		return
	}
	s.projectWorkspace(w, r, p)
}

// projectSummary renders the thin project peek: metadata + per-channel counts,
// served as the peek fragment (?peek=1) or CLI JSON.
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
	now := time.Now().UTC()
	win := store.ResolveRetrievalWindow(r.URL.Query().Get("w"), now)

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
	board, err := store.ProjectsWithCounts(ctx, s.cfg.DB, win, now, s.cfg.SessionIdleTTL)
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
