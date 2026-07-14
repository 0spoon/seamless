package console

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// projectGroupKeys / projectSortKeys are the strict enums the board's toolbar
// GET params accept. An unknown value is a 400 (never a silent default): an
// agent driving the console by URL must see the fix, not a page that quietly
// ignored its param.
var (
	projectGroupKeys = []string{"family", "flat"}
	projectSortKeys  = []string{"recent", "coverage", "name"}
)

// projectRowVM is one board row: a project's strict per-slug health (from
// store.ProjectsWithCounts) merged with its registry metadata (name, parent,
// retired) and a console-side family role. Global is the "" scope row; it is not
// a project and carries no detail link.
type projectRowVM struct {
	Slug         string
	Name         string
	Description  string
	Global       bool
	Child        bool
	Retired      bool
	Unregistered bool
	TileClass    string
	TileIcon     string
	Working      int // live (active) sessions
	Sessions     int
	OpenTasks    int
	Blocked      int
	Memories     int
	Inherited    int // memories a child inherits from its parent's shared briefing
	ReachRate    int
	HasReach     bool // the project has >=1 active memory (a reach denominator)
	LastActive   time.Time
}

// projectGroupVM is a family band on the board: a labeled header plus its rows.
type projectGroupVM struct {
	Label string
	Icon  string
	Count int
	Note  string // e.g. "parent injects 6 memories into 2 children"
	Rows  []projectRowVM
}

// projectsData is the board page payload.
type projectsData struct {
	Group    string
	Sort     string
	Query    string
	Window   string
	Groups   []projectGroupVM // family mode
	Flat     []projectRowVM   // flat mode
	Projects int              // # project rows (excludes the global scope)
	Working  int              // total live sessions across projects
	Families int              // # shared-briefing family bands
}

func (s *Service) projectsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	group := r.URL.Query().Get("group")
	if group == "" {
		group = "family"
	}
	if !slices.Contains(projectGroupKeys, group) {
		s.badRequest(w, r, fmt.Sprintf("invalid group %q: valid values are %s",
			group, strings.Join(projectGroupKeys, ", ")))
		return
	}
	sortKey := r.URL.Query().Get("sort")
	if sortKey == "" {
		sortKey = "recent"
	}
	if !slices.Contains(projectSortKeys, sortKey) {
		s.badRequest(w, r, fmt.Sprintf("invalid sort %q: valid values are %s",
			sortKey, strings.Join(projectSortKeys, ", ")))
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	now := time.Now().UTC()
	win := store.ResolveRetrievalWindow(r.URL.Query().Get("w"), now)

	board, err := store.ProjectsWithCounts(ctx, s.cfg.DB, win, now)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	projects, err := store.ListProjects(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	meta := make(map[string]core.Project, len(projects))
	childrenOf := map[string][]string{}
	isParent := map[string]bool{}
	for _, p := range projects {
		meta[p.Slug] = p
		if p.ParentSlug != "" {
			childrenOf[p.ParentSlug] = append(childrenOf[p.ParentSlug], p.Slug)
			isParent[p.ParentSlug] = true
		}
	}

	// memoriesBySlug lets a child show how many memories it inherits from its
	// parent (the shared briefing injects the parent's active memories).
	memoriesBySlug := make(map[string]int, len(board))
	for _, b := range board {
		memoriesBySlug[b.Project] = b.Memories
	}

	vmBySlug := make(map[string]projectRowVM, len(board))
	var (
		rootRows       []projectRowVM // the global scope
		standaloneRows []projectRowVM
		retiredRows    []projectRowVM
		working        int
		projectCount   int
	)
	q := strings.ToLower(query)
	for _, b := range board {
		p := meta[b.Project]
		vm := newProjectRowVM(b, p, isParent[b.Project])
		if p.ParentSlug != "" {
			vm.Inherited = memoriesBySlug[p.ParentSlug]
		}
		if !matchesQuery(vm, q) {
			continue
		}
		vmBySlug[b.Project] = vm
		working += vm.Working
		if !vm.Global {
			projectCount++
		}
		switch {
		case vm.Global:
			rootRows = append(rootRows, vm)
		case vm.Retired:
			retiredRows = append(retiredRows, vm)
		case vm.Child, isParent[b.Project]:
			// placed into family bands below
		default:
			standaloneRows = append(standaloneRows, vm)
		}
	}

	less := projectLess(sortKey)
	data := projectsData{
		Group: group, Sort: sortKey, Query: query, Window: win.Key,
		Projects: projectCount, Working: working,
	}

	if group == "flat" {
		flat := make([]projectRowVM, 0, len(vmBySlug))
		for _, vm := range vmBySlug {
			vm.Child = false // no indentation in flat mode
			flat = append(flat, vm)
		}
		sort.SliceStable(flat, func(i, j int) bool { return less(flat[i], flat[j]) })
		data.Flat = flat
		s.render(w, r, "projects", pageData{Title: "Projects", Active: "projects", Data: data})
		return
	}

	// Family mode: Root (global) -> each shared-briefing family -> Standalone ->
	// Retired. A family band is a parent row followed by its indented children.
	var groups []projectGroupVM
	if len(rootRows) > 0 {
		sortRows(rootRows, less)
		groups = append(groups, projectGroupVM{
			Label: "Root", Icon: "server", Count: len(rootRows), Rows: rootRows,
		})
	}

	parents := make([]string, 0, len(isParent))
	for slug := range isParent {
		parents = append(parents, slug)
	}
	sort.Strings(parents)
	// Order the family bands by their parent row under the active sort.
	sort.SliceStable(parents, func(i, j int) bool {
		return less(vmBySlug[parents[i]], vmBySlug[parents[j]])
	})
	for _, parent := range parents {
		var rows []projectRowVM
		if pv, ok := vmBySlug[parent]; ok {
			rows = append(rows, pv)
		}
		kids := make([]projectRowVM, 0, len(childrenOf[parent]))
		for _, cs := range childrenOf[parent] {
			if cv, ok := vmBySlug[cs]; ok {
				kids = append(kids, cv)
			}
		}
		sortRows(kids, less)
		rows = append(rows, kids...)
		if len(rows) == 0 {
			continue // whole family filtered out by the query
		}
		groups = append(groups, projectGroupVM{
			Label: parent + " · shared briefing",
			Icon:  "folder-tree",
			Count: len(rows),
			Note:  familyNote(memoriesBySlug[parent], len(kids)),
			Rows:  rows,
		})
		data.Families++
	}

	if len(standaloneRows) > 0 {
		sortRows(standaloneRows, less)
		groups = append(groups, projectGroupVM{
			Label: "Standalone", Icon: "box", Count: len(standaloneRows), Rows: standaloneRows,
		})
	}
	if len(retiredRows) > 0 {
		sortRows(retiredRows, less)
		groups = append(groups, projectGroupVM{
			Label: "Retired by split", Icon: "archive", Count: len(retiredRows), Rows: retiredRows,
		})
	}
	data.Groups = groups
	s.render(w, r, "projects", pageData{Title: "Projects", Active: "projects", Data: data})
}

// newProjectRowVM projects a board row + its registry metadata into a display
// row, assigning a family role and a role-based tile (language is not tracked,
// so the tile reflects structure: parent/global/retired/leaf).
func newProjectRowVM(b store.ProjectBoardRow, p core.Project, parent bool) projectRowVM {
	vm := projectRowVM{
		Slug:         b.Project,
		Name:         p.Name,
		Description:  p.Description,
		Global:       b.Project == "",
		Child:        p.ParentSlug != "",
		Retired:      p.Retired(),
		Unregistered: b.Unregistered,
		Working:      b.LiveSessions,
		Sessions:     b.Sessions,
		OpenTasks:    b.OpenTasks,
		Blocked:      b.Blocked,
		Memories:     b.Memories,
		ReachRate:    b.ReachRate,
		HasReach:     b.Active > 0,
		LastActive:   b.LastActive,
	}
	if vm.Name == "" {
		vm.Name = b.Project
	}
	switch {
	case vm.Global:
		vm.Name = "global"
		vm.TileClass, vm.TileIcon = "grp", "database"
	case vm.Retired:
		vm.TileClass, vm.TileIcon = "", "archive"
	case parent:
		vm.TileClass, vm.TileIcon = "grp", "folder-tree"
	default:
		vm.TileClass, vm.TileIcon = "", "box"
	}
	return vm
}

// familyNote describes a shared-briefing band: the parent injects its active
// memories into each child.
func familyNote(parentMemories, children int) string {
	if children == 0 {
		return ""
	}
	return fmt.Sprintf("parent injects %s into %s",
		plural(parentMemories, "memory", "memories"), plural(children, "child", "children"))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// projectLess builds the row comparator for a sort key: recent = most recent
// activity first, coverage = highest reach first, name = slug ascending.
func projectLess(sortKey string) func(a, b projectRowVM) bool {
	switch sortKey {
	case "coverage":
		return func(a, b projectRowVM) bool {
			if a.ReachRate != b.ReachRate {
				return a.ReachRate > b.ReachRate
			}
			return a.Slug < b.Slug
		}
	case "name":
		return func(a, b projectRowVM) bool { return a.Slug < b.Slug }
	default: // recent
		return func(a, b projectRowVM) bool {
			if !a.LastActive.Equal(b.LastActive) {
				return a.LastActive.After(b.LastActive)
			}
			return a.Slug < b.Slug
		}
	}
}

func sortRows(rows []projectRowVM, less func(a, b projectRowVM) bool) {
	sort.SliceStable(rows, func(i, j int) bool { return less(rows[i], rows[j]) })
}

// matchesQuery reports whether a row matches a lowercased free-text filter over
// its slug, name, and description. An empty query matches everything.
func matchesQuery(vm projectRowVM, q string) bool {
	if q == "" {
		return true
	}
	return strings.Contains(strings.ToLower(vm.Slug), q) ||
		strings.Contains(strings.ToLower(vm.Name), q) ||
		strings.Contains(strings.ToLower(vm.Description), q)
}
