package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

func TestSettingsPage(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	_, err = store.EnsureProject(ctx, db, "seamless", "Seamless")
	require.NoError(t, err)
	_, err = store.EnsureProject(ctx, db, "seam", "Seam CLI")
	require.NoError(t, err)
	require.NoError(t, store.AddRepoMapping(ctx, db, "/Users/x/repos/seamless", "seamless"))
	require.NoError(t, store.SetProjectFamilies(ctx, db, map[string][]string{
		"seam-tools": {"seam", "seamless"},
	}))

	svc, err := New(Config{
		DB: db, APIKey: testKey, DataDir: "/home/.seamless",
		Budgets:     config.Budgets{MaxBriefingTokens: 1500, RecallBudgetTokens: 800},
		GardenerCfg: config.Gardener{Enabled: true, IntervalMinutes: 60, DedupThreshold: 0.88, StalenessDays: 90, DigestDays: 30, SessionIdleMinutes: 45},
		BriefingCfg: config.Defaults().Briefing,
	})
	require.NoError(t, err)
	mux := http.NewServeMux()
	svc.Register(mux)

	var data settingsData
	getJSON(t, mux, "/console/settings?format=json", &data)
	require.Equal(t, "/home/.seamless", data.DataDir)
	require.Equal(t, 1500, data.Budgets.MaxBriefingTokens)
	require.True(t, data.Gardener.Enabled)
	require.Equal(t, 90, data.Gardener.StalenessDays)
	require.Equal(t, 45, data.Gardener.SessionIdleMinutes)
	require.Equal(t, 3, data.Briefing.FindingsCount)
	require.False(t, data.BriefingOverridden)
	require.Len(t, data.Projects, 2)
	require.Len(t, data.RepoMap, 1)
	require.Len(t, data.Families, 1)
	require.Len(t, data.Workspaces, 2)
	require.Len(t, data.FamilyEditors, 1)
	require.Len(t, data.FamilyOptions, 2)
	require.Equal(t, "/Users/x/repos/seamless", data.RepoMap[0].Repo)
	require.Equal(t, "seam", data.Workspaces[0].Slug)
	require.Equal(t, workspaceFamily{Name: "seam-tools", MemberCount: 2}, data.Workspaces[0].Families[0])
	require.Equal(t, familyScopeRef{Slug: "seam", Registered: true}, data.FamilyEditors[0].Members[0])
	require.Equal(t, []string{"/Users/x/repos/seamless"}, data.Workspaces[1].Repos)

	req := httptest.NewRequest(http.MethodGet, "/console/settings", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	page := rr.Body.String()
	require.Contains(t, page, "/Users/x/repos/seamless")
	require.Contains(t, page, "Briefing injection")
	require.Contains(t, page, `class="settings-page" data-settings`)
	require.Contains(t, page, `aria-label="Settings sections"`)
	require.Contains(t, page, `id="runtime-profile"`)
	require.Contains(t, page, `id="briefing-recipe"`)
	require.Contains(t, page, `id="workspace-registry"`)
	require.Contains(t, page, `class="brief-group memory-group"`)
	require.Contains(t, page, `class="brief-group family-group"`)
	require.Contains(t, page, `class="brief-group utility-group"`)
	require.Contains(t, page, `id="utility-mode"`)
	require.Contains(t, page, `class="registry-scroll workspace-directory"`)
	require.Contains(t, page, `class="workspace-row" data-workspace-scope="seamless"`)
	require.Contains(t, page, `class="workspace-route" title="/Users/x/repos/seamless"`)
	require.Contains(t, page, `class="workspace-tag family"`)
	require.Contains(t, page, `class="family-summary"`)
	require.Contains(t, page, `data-dialog-open="family-create-dialog"`)
	require.Contains(t, page, `action="/console/settings/families/save"`)
	require.Contains(t, page, `action="/console/settings/families/delete"`)
	require.NotContains(t, page, `workspace-family-peers`)
	require.NotContains(t, page, `class="repos-panel"`)
	require.NotContains(t, page, `class="families-panel"`)
	require.Contains(t, page, `window.SEAM_NO_LIVE_REFRESH = true`)
	require.Contains(t, page, `data-briefing-form`)
}

func TestSettingsStyles_ControlPlaneContracts(t *testing.T) {
	css := string(consoleCSS)

	require.Contains(t, css, ".settings-jumpbar")
	require.Contains(t, css, "position: sticky")
	require.Contains(t, css, ".briefing-groups")
	require.Contains(t, css, ".brief-group.utility-group {")
	require.Contains(t, css, "grid-column: 1 / -1; display: grid;",
		"the closed-loop card must span the settings grid instead of collapsing to one track")
	require.Contains(t, css, ".brief-number select {")
	require.Contains(t, css, ".is-dirty .brief-state")
	require.Contains(t, css, ".registry-scroll { max-height:")
	require.Contains(t, css, ".workspace-row { display: grid;")
	require.Contains(t, css, ".workspace-cell-label")
	require.Contains(t, css, ".family-summary")
	require.Contains(t, css, ".family-dialog::backdrop")
	require.Contains(t, css, "@media (max-width: 520px)")
}

func TestBuildWorkspaceRegistry_JoinsSourcesAndPreservesReferences(t *testing.T) {
	retiredAt := time.Now().UTC()
	projects := []core.Project{
		{Slug: "app", Name: "App", ParentSlug: "shared"},
		{Slug: "shared", Name: "Shared"},
		{Slug: "old", Name: "Old", RetiredAt: &retiredAt},
	}
	repoMap := map[string]string{
		"/repos/z-app":  "app",
		"/repos/a-app":  "app",
		"/repos/future": "future",
		"/repos/global": "",
	}
	families := map[string][]string{
		"solo":  {"shared"},
		"suite": {"future", "app", "app"},
	}

	workspaces, unbound := buildWorkspaceRegistry(projects, repoMap, families)

	require.Len(t, workspaces, 4)
	require.Equal(t, []string{"app", "shared", "old", "future"}, []string{
		workspaces[0].Slug,
		workspaces[1].Slug,
		workspaces[2].Slug,
		workspaces[3].Slug,
	})
	require.Equal(t, []string{"/repos/a-app", "/repos/z-app"}, workspaces[0].Repos)
	require.True(t, workspaces[0].ParentRegistered)
	require.Equal(t, "suite", workspaces[0].Families[0].Name)
	require.Equal(t, 2, workspaces[0].Families[0].MemberCount)
	require.True(t, workspaces[2].Retired)
	require.False(t, workspaces[3].Registered)
	require.Equal(t, []string{"/repos/future"}, workspaces[3].Repos)
	require.Equal(t, []string{"/repos/global"}, unbound)

	editors, createOptions := buildFamilyEditors(workspaces, families)
	require.Len(t, editors, 2)
	require.Equal(t, "solo", editors[0].Name)
	require.Equal(t, "suite", editors[1].Name)
	require.Equal(t, []familyScopeRef{
		{Slug: "app", Registered: true},
		{Slug: "future", Registered: false},
	}, editors[1].Members)
	require.Len(t, editors[1].Options, 3)
	require.True(t, editors[1].Options[0].Selected)
	require.False(t, editors[1].Options[1].Selected)
	require.True(t, editors[1].Options[2].Selected)
	require.Equal(t, []string{"app", "shared"}, []string{createOptions[0].Slug, createOptions[1].Slug})
}

func TestSettingsFamilyCreateUpdateAndDelete(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	for _, slug := range []string{"app", "backend", "ops"} {
		_, err := store.EnsureProject(ctx, db, slug, slug)
		require.NoError(t, err)
	}

	svc, err := New(Config{DB: db, APIKey: testKey, BriefingCfg: config.Defaults().Briefing})
	require.NoError(t, err)
	mux := http.NewServeMux()
	svc.Register(mux)

	rr := postForm(mux, "/console/settings/families/save", url.Values{
		"name":    {"product"},
		"members": {"app", "backend"},
	}.Encode())
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.Contains(t, rr.Header().Get("Location"), "notice=")
	require.Contains(t, rr.Header().Get("Location"), "#workspace-registry")
	families, err := store.ProjectFamilies(ctx, db)
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"product": {"app", "backend"}}, families)

	// A create using an occupied name errors instead of silently replacing it.
	rr = postForm(mux, "/console/settings/families/save", url.Values{
		"name":    {"product"},
		"members": {"ops"},
	}.Encode())
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.Contains(t, rr.Header().Get("Location"), "error=")
	families, err = store.ProjectFamilies(ctx, db)
	require.NoError(t, err)
	require.Equal(t, []string{"app", "backend"}, families["product"])

	// Editing replaces the member set and can rename the family in one write.
	rr = postForm(mux, "/console/settings/families/save", url.Values{
		"original_name": {"product"},
		"name":          {"platform"},
		"members":       {"backend", "ops"},
	}.Encode())
	require.Equal(t, http.StatusSeeOther, rr.Code)
	families, err = store.ProjectFamilies(ctx, db)
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"platform": {"backend", "ops"}}, families)

	// The picker is a closed set: a forged or stale unknown slug is rejected.
	rr = postForm(mux, "/console/settings/families/save", url.Values{
		"original_name": {"platform"},
		"name":          {"platform"},
		"members":       {"not-a-project"},
	}.Encode())
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.Contains(t, rr.Header().Get("Location"), "error=")
	families, err = store.ProjectFamilies(ctx, db)
	require.NoError(t, err)
	require.Equal(t, []string{"backend", "ops"}, families["platform"])

	rr = postForm(mux, "/console/settings/families/delete", url.Values{
		"original_name": {"platform"},
	}.Encode())
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.Contains(t, rr.Header().Get("Location"), "notice=")
	families, err = store.ProjectFamilies(ctx, db)
	require.NoError(t, err)
	require.Empty(t, families)
}

func TestSettingsBriefingSaveAndReset(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc, err := New(Config{DB: db, APIKey: testKey, BriefingCfg: config.Defaults().Briefing})
	require.NoError(t, err)
	mux := http.NewServeMux()
	svc.Register(mux)

	// Save: stores the override row and redirects back with a notice.
	// include_parent_memories is deliberately absent = unchecked = false.
	rr := postForm(mux, "/console/settings/briefing", url.Values{
		"memory_max_age_days":        {"30"},
		"memory_max_items":           {"20"},
		"findings_count":             {"5"},
		"findings_max_age_days":      {"0"},
		"ready_tasks_shown":          {"1"},
		"pending_plan_max_days":      {"14"},
		"stage_unknown_max_age_days": {"10"},
		"hard_cap_multiplier":        {"2"},
		"sibling_findings_count":     {"0"},
		"include_sibling_memories":   {"1"},
		"utility_weight":             {"0.7"},
		"utility_mode":               {"on"},
	}.Encode())
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.Contains(t, rr.Header().Get("Location"), "notice=")

	var data settingsData
	getJSON(t, mux, "/console/settings?format=json", &data)
	require.True(t, data.BriefingOverridden)
	require.Equal(t, 30, data.Briefing.MemoryMaxAgeDays)
	require.Equal(t, 20, data.Briefing.MemoryMaxItems)
	require.Equal(t, 5, data.Briefing.FindingsCount)
	require.Equal(t, 1, data.Briefing.ReadyTasksShown)
	require.Equal(t, 14, data.Briefing.PendingPlanMaxDays)
	require.Equal(t, 10, data.Briefing.StageUnknownMaxAgeDays)
	require.False(t, data.Briefing.IncludeParentMemories)
	require.True(t, data.Briefing.IncludeSiblingMemories)
	require.Equal(t, 0.7, data.Briefing.UtilityWeight, "the form round-trips the weight, never zeroes it")
	require.Equal(t, "on", data.Briefing.UtilityMode)

	// The saved override is what the retrieval service will read.
	eff, overridden, err := store.BriefingConfig(context.Background(), db, config.Defaults().Briefing)
	require.NoError(t, err)
	require.True(t, overridden)
	require.Equal(t, 5, eff.FindingsCount)

	// Invalid input flashes an error and leaves the stored override untouched.
	rr = postForm(mux, "/console/settings/briefing", "findings_count=-1")
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.Contains(t, rr.Header().Get("Location"), "error=")
	rr = postForm(mux, "/console/settings/briefing", "findings_count=three")
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.Contains(t, rr.Header().Get("Location"), "error=")
	rr = postForm(mux, "/console/settings/briefing", "utility_weight=1.5")
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.Contains(t, rr.Header().Get("Location"), "error=")
	rr = postForm(mux, "/console/settings/briefing", "utility_mode=sideways")
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.Contains(t, rr.Header().Get("Location"), "error=")
	getJSON(t, mux, "/console/settings?format=json", &data)
	require.Equal(t, 5, data.Briefing.FindingsCount)

	// Reset clears the override; effective values fall back to the file/env base.
	rr = postForm(mux, "/console/settings/briefing/reset", "")
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.Contains(t, rr.Header().Get("Location"), "notice=")
	getJSON(t, mux, "/console/settings?format=json", &data)
	require.False(t, data.BriefingOverridden)
	require.Equal(t, 3, data.Briefing.FindingsCount)
	require.True(t, data.Briefing.IncludeParentMemories)
	require.Equal(t, 0.4, data.Briefing.UtilityWeight)
	require.Equal(t, "auto", data.Briefing.UtilityMode)
}

// The per-project force endpoint flips a scope's activation override and the
// Settings payload reflects it; garbage force values flash instead of saving.
func TestSettingsUtilityForce(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc, err := New(Config{DB: db, APIKey: testKey, BriefingCfg: config.Defaults().Briefing})
	require.NoError(t, err)
	mux := http.NewServeMux()
	svc.Register(mux)

	rr := postForm(mux, "/console/settings/utility", url.Values{
		"project": {"seamless"}, "force": {"on"},
	}.Encode())
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.Contains(t, rr.Header().Get("Location"), "notice=")

	var data settingsData
	getJSON(t, mux, "/console/settings?format=json", &data)
	var row *utilityProjectRow
	for i := range data.UtilityRows {
		if data.UtilityRows[i].Project == "seamless" {
			row = &data.UtilityRows[i]
		}
	}
	require.NotNil(t, row, "the forced scope appears in the activation table")
	require.Equal(t, "active", row.Status)
	require.Equal(t, "on", row.Forced)

	// Clearing the force reverts to the latch (unset here -> building).
	rr = postForm(mux, "/console/settings/utility", url.Values{
		"project": {"seamless"}, "force": {"auto"},
	}.Encode())
	require.Equal(t, http.StatusSeeOther, rr.Code)
	a, err := store.GetUtilityActivation(context.Background(), db)
	require.NoError(t, err)
	require.Empty(t, a.Projects["seamless"].Forced)

	// An unrecognized force value is an error, never a silent default.
	rr = postForm(mux, "/console/settings/utility", url.Values{
		"project": {"seamless"}, "force": {"sideways"},
	}.Encode())
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.Contains(t, rr.Header().Get("Location"), "error=")
}
