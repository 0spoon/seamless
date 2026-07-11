package console

import (
	"net/http"
	"sort"

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

// settingsData is the payload for the read-only Settings page.
type settingsData struct {
	DataDir  string          `json:"dataDir"`
	Budgets  config.Budgets  `json:"budgets"`
	Gardener config.Gardener `json:"gardener"`
	Projects []core.Project  `json:"projects"`
	RepoMap  []repoMapping   `json:"repoMap"`
	Families []familyGroup   `json:"families"`
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

	s.render(w, r, "settings", pageData{
		Title:  "Settings",
		Active: "settings",
		Data: settingsData{
			DataDir:  s.cfg.DataDir,
			Budgets:  s.cfg.Budgets,
			Gardener: s.cfg.GardenerCfg,
			Projects: projects,
			RepoMap:  sortedRepoMap(repoMap),
			Families: sortedFamilies(families),
		},
	})
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
