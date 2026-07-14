package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/store"
)

func TestSettingsPage(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	_, err = store.EnsureProject(ctx, db, "seamless", "Seamless")
	require.NoError(t, err)
	require.NoError(t, store.AddRepoMapping(ctx, db, "/Users/x/repos/seamless", "seamless"))

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
	require.Len(t, data.Projects, 1)
	require.Len(t, data.RepoMap, 1)
	require.Equal(t, "/Users/x/repos/seamless", data.RepoMap[0].Repo)

	req := httptest.NewRequest(http.MethodGet, "/console/settings", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "/Users/x/repos/seamless")
	require.Contains(t, rr.Body.String(), "Briefing injection")
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
		"memory_max_age_days":      {"30"},
		"memory_max_items":         {"20"},
		"findings_count":           {"5"},
		"findings_max_age_days":    {"0"},
		"ready_tasks_shown":        {"1"},
		"pending_plan_max_days":    {"14"},
		"hard_cap_multiplier":      {"2"},
		"sibling_findings_count":   {"0"},
		"include_sibling_memories": {"1"},
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
	require.False(t, data.Briefing.IncludeParentMemories)
	require.True(t, data.Briefing.IncludeSiblingMemories)

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
}
