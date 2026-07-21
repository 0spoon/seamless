package console

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
	"github.com/0spoon/seamless/internal/validate"
)

type repoMapping struct {
	Repo    string `json:"repo"`
	Project string `json:"project"`
}

type familyGroup struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

type familyScopeRef struct {
	Slug       string `json:"slug"`
	Registered bool   `json:"registered"`
}

type workspaceFamily struct {
	Name        string `json:"name"`
	MemberCount int    `json:"memberCount"`
}

type familyProjectOption struct {
	Slug       string `json:"slug"`
	Name       string `json:"name,omitempty"`
	Registered bool   `json:"registered"`
	Retired    bool   `json:"retired"`
	Selected   bool   `json:"selected"`
}

type familyEditor struct {
	Name    string                `json:"name"`
	Members []familyScopeRef      `json:"members"`
	Options []familyProjectOption `json:"options"`
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

// utilityProjectRow is one project's utility-activation status for the
// Settings table: where its demand history stands against the readiness
// thresholds, whether the gardener latch has tripped, and any owner force.
type utilityProjectRow struct {
	Project        string     `json:"project"` // "" = global scope
	Status         string     `json:"status"`  // active|armed|building|forced-off
	Forced         string     `json:"forced,omitempty"`
	ReadyAt        *time.Time `json:"readyAt,omitempty"`
	RecentEvents   int        `json:"recentEvents"`
	RecentMemories int        `json:"recentMemories"`
	AgeDays        int        `json:"ageDays"` // days since the first demand event
}

// settingsData is the payload for the Settings page. Briefing carries the
// effective briefing knobs (file/env base + the console override row), while
// FamilyEditors supplies the other intentionally editable control surface.
// Runtime configuration and project routing remain read-only here.
type settingsData struct {
	DataDir            string                `json:"dataDir"`
	Budgets            config.Budgets        `json:"budgets"`
	Gardener           config.Gardener       `json:"gardener"`
	Briefing           config.Briefing       `json:"briefing"`
	BriefingOverridden bool                  `json:"briefingOverridden"`
	UtilityRows        []utilityProjectRow   `json:"utilityRows"`
	UtilityReady       [3]int                `json:"utilityReady"` // thresholds: events, memories, age days
	Projects           []core.Project        `json:"projects"`
	RepoMap            []repoMapping         `json:"repoMap"`
	Families           []familyGroup         `json:"families"`
	Workspaces         []workspaceScope      `json:"workspaces"`
	UnboundRepos       []string              `json:"unboundRepos"`
	FamilyEditors      []familyEditor        `json:"familyEditors"`
	FamilyOptions      []familyProjectOption `json:"familyOptions"`
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
	familyEditors, familyOptions := buildFamilyEditors(workspaces, families)
	briefing, overridden, err := store.BriefingConfig(ctx, s.cfg.DB, s.cfg.BriefingCfg)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	utilityRows, err := s.utilityActivationRows(ctx, projects)
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
			UtilityRows:        utilityRows,
			UtilityReady:       [3]int{store.UtilityReadyMinEvents, store.UtilityReadyMinMemories, store.UtilityReadyMinAgeDays},
			Projects:           projects,
			RepoMap:            sortedRepoMap(repoMap),
			Families:           sortedFamilies(families),
			Workspaces:         workspaces,
			UnboundRepos:       unboundRepos,
			FamilyEditors:      familyEditors,
			FamilyOptions:      familyOptions,
		},
	})
}

// utilityActivationRows joins the activation state with each scope's demand
// progress so the Settings table can say WHY a project is or is not ranked by
// utility yet. Every registered project appears, plus the global scope and any
// slug that exists only in the demand or activation maps.
func (s *Service) utilityActivationRows(ctx context.Context, projects []core.Project) ([]utilityProjectRow, error) {
	now := time.Now().UTC()
	activation, err := store.GetUtilityActivation(ctx, s.cfg.DB)
	if err != nil {
		return nil, err
	}
	demand, err := store.UtilityDemandByProject(ctx, s.cfg.DB, now, store.UtilityReadyWindow)
	if err != nil {
		return nil, err
	}

	slugs := map[string]struct{}{"": {}}
	for _, p := range projects {
		if !p.Retired() {
			slugs[p.Slug] = struct{}{}
		}
	}
	for slug := range demand {
		slugs[slug] = struct{}{}
	}
	for slug := range activation.Projects {
		slugs[slug] = struct{}{}
	}

	rows := make([]utilityProjectRow, 0, len(slugs))
	for slug := range slugs {
		st := activation.Projects[slug]
		d := demand[slug]
		row := utilityProjectRow{
			Project: slug, Forced: st.Forced, ReadyAt: st.ReadyAt,
			RecentEvents: d.RecentEvents, RecentMemories: d.RecentMemories,
		}
		if !d.Earliest.IsZero() {
			row.AgeDays = int(now.Sub(d.Earliest).Hours() / 24)
		}
		switch {
		case st.Forced == "off":
			row.Status = "forced-off"
		case st.Forced == "on" || st.ReadyAt != nil:
			row.Status = "active"
		case d.Ready(now):
			row.Status = "armed" // latches on the next gardener pass
		default:
			row.Status = "building"
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if (rows[i].Status == "active") != (rows[j].Status == "active") {
			return rows[i].Status == "active"
		}
		if rows[i].RecentEvents != rows[j].RecentEvents {
			return rows[i].RecentEvents > rows[j].RecentEvents
		}
		return rows[i].Project < rows[j].Project
	})
	return rows, nil
}

// settingsUtilityForce sets or clears the owner's per-project force: "on" and
// "off" win over the gardener latch; "auto" clears the force and defers to it.
func (s *Service) settingsUtilityForce(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		settingsFlash(w, r, "could not read the form")
		return
	}
	project := strings.TrimSpace(r.PostFormValue("project"))
	if project != "" {
		if err := validate.Name(project); err != nil {
			settingsFlash(w, r, "project: "+err.Error())
			return
		}
	}
	force := r.PostFormValue("force")
	switch force {
	case "on", "off", "auto":
	default:
		settingsFlash(w, r, fmt.Sprintf("force invalid %q: valid values are on, off, auto", force))
		return
	}

	ctx := r.Context()
	activation, err := store.GetUtilityActivation(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	st := activation.Projects[project]
	if force == "auto" {
		st.Forced = ""
	} else {
		st.Forced = force
	}
	activation.Projects[project] = st
	if err := store.SetUtilityActivation(ctx, s.cfg.DB, activation); err != nil {
		s.serverError(w, r, err)
		return
	}
	scope := project
	if scope == "" {
		scope = "the global scope"
	}
	settingsNotice(w, r, fmt.Sprintf("Utility ranking for %s set to %s.", scope, force))
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
		UtilityMode:            r.PostFormValue("utility_mode"),
	}
	weightStr := strings.TrimSpace(r.PostFormValue("utility_weight"))
	if weightStr == "" {
		weightStr = "0"
	}
	weight, err := strconv.ParseFloat(weightStr, 64)
	if err != nil {
		settingsFlash(w, r, "utility_weight must be a number between 0 and 1")
		return
	}
	b.UtilityWeight = weight
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

// settingsFamilySave creates a family or replaces one family's name and member
// set. The picker submits a closed list of known project slugs; accepting an
// arbitrary slug here would let a typo create a plausible but inert family
// member that never contributes context.
func (s *Service) settingsFamilySave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		settingsRegistryFlash(w, r, "Could not read the family form.")
		return
	}

	names, ok := r.PostForm["name"]
	if !ok || len(names) != 1 {
		settingsRegistryFlash(w, r, "Enter one family name.")
		return
	}
	name := strings.TrimSpace(names[0])
	if err := validate.Name(name); err != nil {
		settingsRegistryFlash(w, r, "Family name: "+err.Error())
		return
	}

	previousName := ""
	if previous, present := r.PostForm["original_name"]; present {
		if len(previous) != 1 || strings.TrimSpace(previous[0]) == "" {
			settingsRegistryFlash(w, r, "The family being edited is missing.")
			return
		}
		previousName = strings.TrimSpace(previous[0])
		if err := validate.Name(previousName); err != nil {
			settingsRegistryFlash(w, r, "Original family name: "+err.Error())
			return
		}
	}

	members := uniqueNonEmpty(r.PostForm["members"])
	if len(members) == 0 {
		settingsRegistryFlash(w, r, "Choose at least one project for the family.")
		return
	}
	projects, err := store.ListProjects(r.Context(), s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	families, err := store.ProjectFamilies(r.Context(), s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	allowed := make(map[string]bool, len(projects)+len(members))
	for _, project := range projects {
		allowed[project.Slug] = true
	}
	if previousName != "" {
		currentMembers, exists := families[previousName]
		if !exists {
			settingsRegistryFlash(w, r, fmt.Sprintf("Family %q no longer exists.", previousName))
			return
		}
		for _, member := range currentMembers {
			allowed[member] = true
		}
	}
	for _, member := range members {
		if err := validate.Name(member); err != nil {
			settingsRegistryFlash(w, r, "Project slug: "+err.Error())
			return
		}
		if !allowed[member] {
			settingsRegistryFlash(w, r, fmt.Sprintf("Unknown project scope %q.", member))
			return
		}
	}

	_, err = store.SaveProjectFamily(r.Context(), s.cfg.DB, previousName, name, members)
	switch {
	case errors.Is(err, store.ErrFamilyExists):
		settingsRegistryFlash(w, r, fmt.Sprintf("A family named %q already exists.", name))
		return
	case errors.Is(err, store.ErrFamilyNotFound):
		settingsRegistryFlash(w, r, fmt.Sprintf("Family %q no longer exists.", previousName))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	verb := "created"
	if previousName != "" {
		verb = "updated"
	}
	settingsRegistryNotice(w, r, fmt.Sprintf("Family %q %s.", name, verb))
}

func (s *Service) settingsFamilyDelete(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		settingsRegistryFlash(w, r, "Could not read the family form.")
		return
	}
	names, ok := r.PostForm["original_name"]
	if !ok || len(names) != 1 {
		settingsRegistryFlash(w, r, "Choose one family to delete.")
		return
	}
	name := strings.TrimSpace(names[0])
	if err := validate.Name(name); err != nil {
		settingsRegistryFlash(w, r, "Family name: "+err.Error())
		return
	}
	_, err := store.RemoveFamilyMembers(r.Context(), s.cfg.DB, name, nil)
	switch {
	case errors.Is(err, store.ErrFamilyNotFound):
		settingsRegistryFlash(w, r, fmt.Sprintf("Family %q no longer exists.", name))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	settingsRegistryNotice(w, r, fmt.Sprintf("Family %q deleted.", name))
}

func settingsFlash(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/console/settings?error="+url.QueryEscape(msg), http.StatusSeeOther)
}

func settingsNotice(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/console/settings?notice="+url.QueryEscape(msg), http.StatusSeeOther)
}

func settingsRegistryFlash(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/console/settings?error="+url.QueryEscape(msg)+"#workspace-registry", http.StatusSeeOther)
}

func settingsRegistryNotice(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/console/settings?notice="+url.QueryEscape(msg)+"#workspace-registry", http.StatusSeeOther)
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
			bySlug[slug].Families = append(bySlug[slug].Families, workspaceFamily{
				Name:        family.Name,
				MemberCount: len(members),
			})
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

func buildFamilyEditors(workspaces []workspaceScope, families map[string][]string) ([]familyEditor, []familyProjectOption) {
	bySlug := make(map[string]workspaceScope, len(workspaces))
	createOptions := make([]familyProjectOption, 0, len(workspaces))
	for _, workspace := range workspaces {
		bySlug[workspace.Slug] = workspace
		if workspace.Registered && !workspace.Retired {
			createOptions = append(createOptions, familyProjectOption{
				Slug:       workspace.Slug,
				Name:       workspace.Name,
				Registered: true,
			})
		}
	}

	groups := sortedFamilies(families)
	editors := make([]familyEditor, 0, len(groups))
	for _, group := range groups {
		members := uniqueNonEmpty(group.Members)
		selected := make(map[string]bool, len(members))
		editor := familyEditor{
			Name:    group.Name,
			Members: make([]familyScopeRef, 0, len(members)),
		}
		for _, slug := range members {
			selected[slug] = true
			editor.Members = append(editor.Members, familyScopeRef{
				Slug:       slug,
				Registered: bySlug[slug].Registered,
			})
		}
		for _, workspace := range workspaces {
			if (!workspace.Registered || workspace.Retired) && !selected[workspace.Slug] {
				continue
			}
			editor.Options = append(editor.Options, familyProjectOption{
				Slug:       workspace.Slug,
				Name:       workspace.Name,
				Registered: workspace.Registered,
				Retired:    workspace.Retired,
				Selected:   selected[workspace.Slug],
			})
		}
		editors = append(editors, editor)
	}
	return editors, createOptions
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
