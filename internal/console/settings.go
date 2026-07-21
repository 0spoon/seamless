package console

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

type repoMapping struct {
	Repo    string `json:"repo"`
	Project string `json:"project"`
}

type familyGroup struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

type workspaceFamilyMember struct {
	Slug       string `json:"slug"`
	Registered bool   `json:"registered"`
}

type workspaceFamily struct {
	Name        string                  `json:"name"`
	MemberCount int                     `json:"memberCount"`
	Peers       []workspaceFamilyMember `json:"peers"`
}

// workspaceScope joins the three ways Settings describes a scope: its project
// row, the repo paths that resolve to it, and the families that share context
// with it. Registered is false for slugs referenced only by routing, lineage,
// or family metadata, so the console can expose those dangling references
// instead of silently dropping them from the unified directory.
type workspaceScope struct {
	Slug             string            `json:"slug"`
	Name             string            `json:"name,omitempty"`
	Description      string            `json:"description,omitempty"`
	ParentSlug       string            `json:"parentSlug,omitempty"`
	Registered       bool              `json:"registered"`
	Retired          bool              `json:"retired"`
	ParentRegistered bool              `json:"parentRegistered"`
	Repos            []string          `json:"repos"`
	Families         []workspaceFamily `json:"families"`
}

// settingsData is the payload for the Settings page. Briefing carries the
// effective briefing knobs (file/env base + the console override row); it is
// the one editable block -- everything else stays a read-only view of the
// running configuration.
type settingsData struct {
	DataDir            string           `json:"dataDir"`
	Budgets            config.Budgets   `json:"budgets"`
	Gardener           config.Gardener  `json:"gardener"`
	Briefing           config.Briefing  `json:"briefing"`
	BriefingOverridden bool             `json:"briefingOverridden"`
	Projects           []core.Project   `json:"projects"`
	RepoMap            []repoMapping    `json:"repoMap"`
	Families           []familyGroup    `json:"families"`
	Workspaces         []workspaceScope `json:"workspaces"`
	UnboundRepos       []string         `json:"unboundRepos"`
}

func (s *Service) settings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	projects, err := store.ListProjects(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	repoMap, err := store.RepoProjectMap(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	families, err := store.ProjectFamilies(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	workspaces, unboundRepos := buildWorkspaceRegistry(projects, repoMap, families)
	briefing, overridden, err := store.BriefingConfig(ctx, s.cfg.DB, s.cfg.BriefingCfg)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	s.render(w, r, "settings", pageData{
		Title:  "Settings",
		Active: "settings",
		Data: settingsData{
			DataDir:            s.cfg.DataDir,
			Budgets:            s.cfg.Budgets,
			Gardener:           s.cfg.GardenerCfg,
			Briefing:           briefing,
			BriefingOverridden: overridden,
			Projects:           projects,
			RepoMap:            sortedRepoMap(repoMap),
			Families:           sortedFamilies(families),
			Workspaces:         workspaces,
			UnboundRepos:       unboundRepos,
		},
	})
}

// settingsBriefingSave persists the briefing form as the runtime override row
// (store.SettingBriefingConfig). It never writes the config file; the override
// layers over file/env values and takes effect on the next session start, so no
// daemon restart is needed. Redirects back with a flash either way.
func (s *Service) settingsBriefingSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		settingsFlash(w, r, "could not read the form")
		return
	}
	b := config.Briefing{
		IncludeParentMemories:  r.PostFormValue("include_parent_memories") != "",
		IncludeSiblingMemories: r.PostFormValue("include_sibling_memories") != "",
	}
	for name, dst := range map[string]*int{
		"memory_max_age_days":        &b.MemoryMaxAgeDays,
		"memory_max_items":           &b.MemoryMaxItems,
		"findings_count":             &b.FindingsCount,
		"findings_max_age_days":      &b.FindingsMaxAgeDays,
		"ready_tasks_shown":          &b.ReadyTasksShown,
		"pending_plan_max_days":      &b.PendingPlanMaxDays,
		"stage_unknown_max_age_days": &b.StageUnknownMaxAgeDays,
		"hard_cap_multiplier":        &b.HardCapMultiplier,
		"sibling_findings_count":     &b.SiblingFindingsCount,
	} {
		v := strings.TrimSpace(r.PostFormValue(name))
		if v == "" {
			v = "0"
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			settingsFlash(w, r, name+" must be a whole number")
			return
		}
		*dst = n
	}
	if err := b.Validate(); err != nil {
		settingsFlash(w, r, err.Error())
		return
	}
	if err := store.SetBriefingConfig(r.Context(), s.cfg.DB, b); err != nil {
		s.serverError(w, r, err)
		return
	}
	settingsNotice(w, r, "Briefing settings saved -- they apply from the next session start.")
}

// settingsBriefingReset clears the runtime override row, reverting the
// effective briefing knobs to the file/env configuration.
func (s *Service) settingsBriefingReset(w http.ResponseWriter, r *http.Request) {
	if err := store.ClearBriefingConfig(r.Context(), s.cfg.DB); err != nil {
		s.serverError(w, r, err)
		return
	}
	settingsNotice(w, r, "Briefing overrides cleared -- back to the file/env configuration.")
}

func settingsFlash(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/console/settings?error="+url.QueryEscape(msg), http.StatusSeeOther)
}

func settingsNotice(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/console/settings?notice="+url.QueryEscape(msg), http.StatusSeeOther)
}

func sortedRepoMap(m map[string]string) []repoMapping {
	out := make([]repoMapping, 0, len(m))
	for repo, project := range m {
		out = append(out, repoMapping{Repo: repo, Project: project})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo < out[j].Repo })
	return out
}

func sortedFamilies(m map[string][]string) []familyGroup {
	out := make([]familyGroup, 0, len(m))
	for name, members := range m {
		sorted := append([]string(nil), members...)
		sort.Strings(sorted)
		out = append(out, familyGroup{Name: name, Members: sorted})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func buildWorkspaceRegistry(projects []core.Project, repoMap map[string]string, families map[string][]string) ([]workspaceScope, []string) {
	bySlug := make(map[string]*workspaceScope, len(projects))
	ensure := func(slug string) *workspaceScope {
		if scope, ok := bySlug[slug]; ok {
			return scope
		}
		scope := &workspaceScope{Slug: slug}
		bySlug[slug] = scope
		return scope
	}

	for _, project := range projects {
		slug := strings.TrimSpace(project.Slug)
		if slug == "" {
			continue
		}
		scope := ensure(slug)
		scope.Name = project.Name
		scope.Description = project.Description
		scope.ParentSlug = strings.TrimSpace(project.ParentSlug)
		scope.Registered = true
		scope.Retired = project.Retired()
	}
	for _, project := range projects {
		if parent := strings.TrimSpace(project.ParentSlug); parent != "" {
			ensure(parent)
		}
	}

	var unboundRepos []string
	for repo, project := range repoMap {
		slug := strings.TrimSpace(project)
		if slug == "" {
			unboundRepos = append(unboundRepos, repo)
			continue
		}
		scope := ensure(slug)
		scope.Repos = append(scope.Repos, repo)
	}

	for _, family := range sortedFamilies(families) {
		members := uniqueNonEmpty(family.Members)
		for _, slug := range members {
			ensure(slug)
		}
		for _, slug := range members {
			membership := workspaceFamily{
				Name:        family.Name,
				MemberCount: len(members),
				Peers:       make([]workspaceFamilyMember, 0, len(members)-1),
			}
			for _, peer := range members {
				if peer == slug {
					continue
				}
				membership.Peers = append(membership.Peers, workspaceFamilyMember{
					Slug:       peer,
					Registered: bySlug[peer].Registered,
				})
			}
			bySlug[slug].Families = append(bySlug[slug].Families, membership)
		}
	}

	out := make([]workspaceScope, 0, len(bySlug))
	for _, scope := range bySlug {
		sort.Strings(scope.Repos)
		sort.Slice(scope.Families, func(i, j int) bool {
			return scope.Families[i].Name < scope.Families[j].Name
		})
		if scope.ParentSlug != "" {
			scope.ParentRegistered = bySlug[scope.ParentSlug].Registered
		}
		out = append(out, *scope)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Registered != out[j].Registered {
			return out[i].Registered
		}
		if out[i].Retired != out[j].Retired {
			return !out[i].Retired
		}
		return out[i].Slug < out[j].Slug
	})
	sort.Strings(unboundRepos)
	return out, unboundRepos
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
