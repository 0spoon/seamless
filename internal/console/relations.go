package console

// The Relations screen (/console/relations) is the standalone view of the
// dependency spine the flat console can't show: a plan expands into its steps,
// each step's claiming session, and the memories that session left behind. It
// reuses the project-detail tree + banner builders (buildProjectTree,
// projectBanners, splitLineageBanner) and adds a scope selector so the tree can
// span every project or narrow to one.

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// relationsScopeKeys are the strict scope enum for /console/relations: "all"
// renders every project's spine on one page; "project" narrows to a single
// project named by ?project=<slug>. An unknown scope is a 400 (naming the valid
// values); an unknown project slug is a 404 (naming the slug) -- an agent
// driving the console by URL sees the fix, never a silent default. A RETIRED
// project is not a 404: it still renders (with its retired banner), kept for
// provenance.
var relationsScopeKeys = []string{"all", "project"}

// relationsData is the /console/relations page payload.
type relationsData struct {
	Scope    string
	Project  string // selected slug when Scope == "project"
	Options  []relScopeOption
	Sections []relSection
	Banners  []relBanner
	Plans    int // total plan trees rendered
}

// relScopeOption is one entry in the scope selector segment.
type relScopeOption struct {
	Label  string
	Href   string
	Active bool
}

// relSection is one project's block: its plan -> step -> session -> memory tree,
// with a header shown only in all-projects mode (where several projects' trees
// share the page and a plan slug is unique only per project, so the tree must be
// keyed by (project, slug)).
type relSection struct {
	Project   string
	Name      string
	Href      string
	Retired   bool
	TileClass string
	TileIcon  string
	Header    bool
	Tree      []treeNode
}

func (s *Service) relations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "all"
	}
	if !slices.Contains(relationsScopeKeys, scope) {
		s.badRequest(w, r, fmt.Sprintf("invalid scope %q: valid values are %s",
			scope, strings.Join(relationsScopeKeys, ", ")))
		return
	}

	projects, err := store.ListProjects(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	isParent := map[string]bool{}
	for _, p := range projects {
		if p.ParentSlug != "" {
			isParent[p.ParentSlug] = true
		}
	}

	// The scope selector lists every project that has at least one plan (a
	// project with no plans has nothing to trace), plus the "all projects" entry.
	planned := make([]core.Project, 0, len(projects))
	for _, p := range projects {
		slugs, err := store.DistinctPlanSlugsForProject(ctx, s.cfg.DB, p.Slug)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if len(slugs) > 0 {
			planned = append(planned, p)
		}
	}

	data := relationsData{Scope: scope}

	switch scope {
	case "project":
		slug := strings.TrimSpace(r.URL.Query().Get("project"))
		if slug == "" {
			s.badRequest(w, r, "scope=project requires a ?project=<slug> param naming the project to trace")
			return
		}
		p, ok, err := store.ProjectBySlug(ctx, s.cfg.DB, slug)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if !ok {
			s.notFound(w, r, fmt.Sprintf("unknown project %q", slug))
			return
		}
		data.Project = slug
		tree, err := s.buildProjectTree(ctx, slug)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		cls, ic := relationTile(p, isParent[slug])
		data.Sections = []relSection{{
			Project: slug, Name: projectDisplayName(p), Retired: p.Retired(),
			TileClass: cls, TileIcon: ic, Tree: tree,
		}}
		data.Plans = len(tree)

		banners, err := s.projectBanners(ctx, p)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		fam, err := s.familyBanner(ctx, slug)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if fam != nil {
			banners = append(banners, *fam)
		}
		data.Banners = banners

	default: // all
		for _, p := range planned {
			tree, err := s.buildProjectTree(ctx, p.Slug)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			if len(tree) == 0 {
				continue
			}
			cls, ic := relationTile(p, isParent[p.Slug])
			data.Sections = append(data.Sections, relSection{
				Project: p.Slug, Name: projectDisplayName(p), Retired: p.Retired(),
				Href: relationsProjectHref(p.Slug), TileClass: cls, TileIcon: ic,
				Header: true, Tree: tree,
			})
			data.Plans += len(tree)
		}
		banners, err := s.crossProjectBanners(ctx, projects)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data.Banners = banners
	}

	data.Options = relationsOptions(scope, data.Project, planned)
	s.render(w, r, "relations", pageData{Title: "Relations", Active: "relations", Data: data})
}

// relationsOptions builds the scope selector: "All projects" plus one entry per
// project that has a plan, flagging the active selection.
func relationsOptions(scope, selected string, planned []core.Project) []relScopeOption {
	opts := []relScopeOption{{
		Label: "All projects", Href: "/console/relations", Active: scope == "all",
	}}
	for _, p := range planned {
		opts = append(opts, relScopeOption{
			Label:  p.Slug,
			Href:   relationsProjectHref(p.Slug),
			Active: scope == "project" && p.Slug == selected,
		})
	}
	return opts
}

// relationsProjectHref is the single-project deep-link for a slug.
func relationsProjectHref(slug string) string {
	return "/console/relations?scope=project&project=" + url.QueryEscape(slug)
}

// projectDisplayName is a project's display name, falling back to its slug.
func projectDisplayName(p core.Project) string {
	if p.Name != "" {
		return p.Name
	}
	return p.Slug
}

// relationTile picks a section tile by structural role (language is not tracked):
// retired -> archive, a shared-briefing parent -> folder-tree, else a leaf box.
func relationTile(p core.Project, isParent bool) (class, ic string) {
	switch {
	case p.Retired():
		return "", "archive"
	case isParent:
		return "grp", "folder-tree"
	default:
		return "", "box"
	}
}

// crossProjectBanners builds the all-projects "cross-project ties": every
// shared-briefing parent (injects its memories into its children), every sibling
// family (project_families members that share briefings without a parent/child
// edge), and every retired project's split-lineage note. Single-project mode
// uses projectBanners + familyBanner instead.
func (s *Service) crossProjectBanners(ctx context.Context, projects []core.Project) ([]relBanner, error) {
	childrenOf := map[string][]core.Project{}
	for _, p := range projects {
		if p.ParentSlug != "" {
			childrenOf[p.ParentSlug] = append(childrenOf[p.ParentSlug], p)
		}
	}
	var banners []relBanner
	for _, p := range projects {
		kids := childrenOf[p.Slug]
		if p.Retired() || len(kids) == 0 {
			continue
		}
		counts, err := store.GetProjectCounts(ctx, s.cfg.DB, p.Slug)
		if err != nil {
			return nil, err
		}
		banners = append(banners, relBanner{Icon: "git-branch", HTML: template.HTML(fmt.Sprintf(
			`<b>%s</b> shares %s into %s at briefing time &mdash; children inherit without duplicating.`,
			template.HTMLEscapeString(p.Slug), plural(counts.Memories, "active memory", "active memories"),
			childList(kids)))})
	}

	families, err := store.ProjectFamilies(ctx, s.cfg.DB)
	if err != nil {
		return nil, err
	}
	famNames := make([]string, 0, len(families))
	for name := range families {
		famNames = append(famNames, name)
	}
	sort.Strings(famNames)
	for _, name := range famNames {
		if len(families[name]) < 2 {
			continue
		}
		banners = append(banners, relBanner{Icon: "git-branch", HTML: template.HTML(fmt.Sprintf(
			`family <b>%s</b> links %s &mdash; each sees the others' recent findings at briefing time.`,
			template.HTMLEscapeString(name), boldList(families[name])))})
	}

	for _, p := range projects {
		if !p.Retired() {
			continue
		}
		b, err := s.splitLineageBanner(ctx, p)
		if err != nil {
			return nil, err
		}
		if b != nil {
			banners = append(banners, *b)
		}
	}
	return banners, nil
}

// familyBanner is the sibling-family tie for a single project: the other members
// of any project_families it belongs to. Returns nil when the project has no
// family siblings.
func (s *Service) familyBanner(ctx context.Context, slug string) (*relBanner, error) {
	sibs, err := store.SiblingProjects(ctx, s.cfg.DB, slug)
	if err != nil {
		return nil, err
	}
	if len(sibs) == 0 {
		return nil, nil
	}
	return &relBanner{Icon: "git-branch", HTML: template.HTML(fmt.Sprintf(
		`<b>%s</b> shares a briefing family with %s &mdash; each sees the others' recent findings.`,
		template.HTMLEscapeString(slug), boldList(sibs)))}, nil
}
