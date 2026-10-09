package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arctop/seamless/internal/config"
	"github.com/arctop/seamless/internal/core"
	"github.com/arctop/seamless/internal/events"
	"github.com/arctop/seamless/internal/store"
	"github.com/arctop/seamless/internal/update"
	"github.com/stretchr/testify/require"
)

// asRelease makes this test binary look like a goreleaser build of v for the
// test's duration. No test in this package runs in parallel, so swapping the
// link-time variables is safe.
func asRelease(t *testing.T, v string) {
	t.Helper()
	oldV, oldD := version, distribution
	version, distribution = v, update.DistributionRelease
	t.Cleanup(func() { version, distribution = oldV, oldD })
}

// testUpdateDeps is the daemon's checker wiring over a temp data dir, a real
// SQLite event log, and a TLS release server, with the loop's tick held.
func testUpdateDeps(t *testing.T, list string) (update.Deps, *events.Recorder, *sql.DB, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `W/"1"`)
		_, _ = w.Write([]byte(list))
	}))
	t.Cleanup(srv.Close)
	client := srv.Client()
	client.CheckRedirect = update.HTTPSOnlyRedirect

	db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	rec := events.NewRecorder(db)

	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	deps := updateCheckerDeps(cfg, db, rec, "01TESTINSTANCE",
		update.Install{Kind: update.KindInstaller, Hint: update.Hint(update.KindInstaller)}, nil)
	deps.Fetcher = &update.Fetcher{Client: client, URL: srv.URL, Now: time.Now}
	ticks := make(chan time.Time)
	deps.Ticks = func() (<-chan time.Time, func()) { return ticks, func() {} }
	return deps, rec, db, cfg.DataDir
}

const oneNewerRelease = `[{"tag_name":"v0.7.3","published_at":"2026-10-08T00:00:00Z","assets":[
  {"name":"checksums.txt","state":"uploaded"},{"name":"install","state":"uploaded"},
  {"name":"install.sigstore.json","state":"uploaded"},{"name":"install.ps1","state":"uploaded"},
  {"name":"install.ps1.sigstore.json","state":"uploaded"}]}]`

// TestUpdateWiring_ReleaseBuildEndToEnd runs the real checker against a real
// event log and the real fetcher: a check records update.available once,
// writes the state file, and /healthz then names the newer release -- until
// the console turns checks off.
func TestUpdateWiring_ReleaseBuildEndToEnd(t *testing.T) {
	asRelease(t, "0.7.2")
	deps, rec, db, dataDir := testUpdateDeps(t, oneNewerRelease)
	chk := update.New(deps)
	ctx, cancel := context.WithCancel(context.Background())
	chk.Start(ctx)
	t.Cleanup(func() { cancel(); chk.Wait() })

	st, err := chk.CheckNow(ctx)
	require.NoError(t, err)
	require.True(t, st.Available)
	require.Equal(t, "0.7.3", st.Newest.Version.String())

	saved, err := update.ReadState(dataDir)
	require.NoError(t, err)
	require.Equal(t, "01TESTINSTANCE", saved.Running.Instance)
	require.Equal(t, "0.7.2", saved.LastRunVersion.String())
	require.Len(t, saved.Releases, 1)

	got, err := rec.ByKinds(ctx, []core.EventKind{core.EventUpdateAvailable}, "", "", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "0.7.3", got[0].ItemID)

	body := serveHealthz(t, db, deps.Instance, chk)
	require.Equal(t, "0.7.3", body["update_available"])
	require.Equal(t, "01TESTINSTANCE", body["instance"])
	require.Equal(t, "release", body["distribution"])

	// The console turns checks off: /healthz stops naming the release.
	off := false
	require.NoError(t, store.SetUpdateOverride(ctx, db, config.UpdateOverride{Check: &off}))
	_, err = chk.Refresh(ctx)
	require.NoError(t, err)
	_, has := serveHealthz(t, db, deps.Instance, chk)["update_available"]
	require.False(t, has)
}

// TestUpdateWiring_SourceBuildNeverWritesState: a source build -- which every
// test, make run, the fixtures and seambench are -- never asks GitHub by
// default and never creates <data_dir>/update.
func TestUpdateWiring_SourceBuildNeverWritesState(t *testing.T) {
	deps, rec, db, dataDir := testUpdateDeps(t, oneNewerRelease)
	chk := update.New(deps)
	ctx, cancel := context.WithCancel(context.Background())
	chk.Start(ctx)
	t.Cleanup(func() { cancel(); chk.Wait() })

	st := chk.Status()
	require.False(t, st.Release)
	require.False(t, st.Settings.Check)
	_, err := chk.CheckNow(ctx)
	require.ErrorIs(t, err, update.ErrChecksOff)

	_, err = os.Stat(update.StateDir(dataDir))
	require.True(t, os.IsNotExist(err))
	got, err := rec.ByKinds(ctx, []core.EventKind{core.EventUpdateAvailable, core.EventUpdateApplied}, "", "", 10)
	require.NoError(t, err)
	require.Empty(t, got)

	body := serveHealthz(t, db, deps.Instance, chk)
	require.Equal(t, "source", body["distribution"])
	_, has := body["update_available"]
	require.False(t, has)
}

func TestHealthzBody(t *testing.T) {
	newest := &update.Release{Version: update.Version{Major: 0, Minor: 7, Patch: 3}}
	on := update.Settings{Check: true}
	tests := []struct {
		name      string
		st        *update.Status
		instance  string
		wantAvail string
	}{
		{"no update check wired", nil, "", ""},
		{"up to date", &update.Status{Settings: on, Newest: newest}, "01I", ""},
		{"available", &update.Status{Settings: on, Newest: newest, Available: true}, "01I", "0.7.3"},
		{"available but checks off", &update.Status{Newest: newest, Available: true}, "01I", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := healthzBody("ok", tt.instance, tt.st)
			require.Equal(t, "ok", body["status"])
			require.Equal(t, buildVersion(), body["version"])
			require.Equal(t, distribution, body["distribution"])
			require.Equal(t, tt.instance, body["instance"])
			require.Equal(t, tt.wantAvail, body["update_available"])
			if tt.instance == "" {
				_, has := body["instance"]
				require.False(t, has)
			}
		})
	}
}

// serveHealthz runs the real /healthz handler once against the checker.
func serveHealthz(t *testing.T, db *sql.DB, instance string, chk *update.Checker) map[string]string {
	t.Helper()
	w := httptest.NewRecorder()
	healthzHandler(db, instance, chk.Status)(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}
