package console

import (
	"context"
	"net/http"
	"net/http/httptest"
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
		GardenerCfg: config.Gardener{Enabled: true, IntervalMinutes: 60, DedupThreshold: 0.88, StalenessDays: 90, DigestDays: 30},
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
	require.Len(t, data.Projects, 1)
	require.Len(t, data.RepoMap, 1)
	require.Equal(t, "/Users/x/repos/seamless", data.RepoMap[0].Repo)

	req := httptest.NewRequest(http.MethodGet, "/console/settings", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "/Users/x/repos/seamless")
}
