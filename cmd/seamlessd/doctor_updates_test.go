package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arctop/seamless/internal/config"
	"github.com/arctop/seamless/internal/store"
	"github.com/arctop/seamless/internal/update"
	"github.com/stretchr/testify/require"
)

// releaseRecord is the state a release daemon on v0.7.2 writes.
func releaseRecord(now time.Time, mutate func(s *update.State)) update.State {
	v := update.Version{Major: 0, Minor: 7, Patch: 2}
	s := update.State{
		CheckedAt:   now.Add(-2 * time.Hour),
		NextCheckAt: now.Add(4 * time.Hour),
		Releases:    []update.Release{{Version: v, PublishedAt: now.Add(-72 * time.Hour)}},
		Running: update.Running{
			Version: v, Distribution: update.DistributionRelease, Kind: update.KindInstaller,
			Instance: "01INST", PID: 7, StartedAt: now.Add(-3 * time.Hour),
		},
		LastRunVersion: &v,
	}
	if mutate != nil {
		mutate(&s)
	}
	return s
}

func TestUpdatesCheck(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	no := false
	newer := func(s *update.State) {
		s.Releases = append(s.Releases, update.Release{Version: update.Version{Major: 0, Minor: 7, Patch: 3}, PublishedAt: now.Add(-time.Hour)})
	}
	tests := []struct {
		name       string
		state      *update.State // nil: no state file
		corrupt    bool
		check      *bool
		wantStatus checkStatus
		want       string
	}{
		{"source build with nothing recorded", nil, false, nil, statusInfo, "builds from source"},
		{"release daemon, up to date", ptr(releaseRecord(now, nil)), false, nil, statusOK, "up to date at v0.7.2 (checked 2h ago"},
		{"release daemon, behind", ptr(releaseRecord(now, newer)), false, nil, statusWarn,
			"v0.7.3 is available (running v0.7.2; checked 2h ago) -- update with: seamlessd update"},
		{"homebrew daemon, behind", ptr(releaseRecord(now, func(s *update.State) {
			newer(s)
			s.Running.Kind = update.KindHomebrew
		})), false, nil, statusWarn, "brew upgrade --cask arctop/tap/seamless"},
		{"checks off in the config", ptr(releaseRecord(now, newer)), false, &no, statusInfo, "update.check: false"},
		{"a fresh error streak informs", ptr(releaseRecord(now, func(s *update.State) {
			s.CheckError = &update.CheckError{Kind: update.CheckErrorUnavailable, Message: "dial tcp: timeout", Since: now.Add(-time.Hour), Count: 2}
		})), false, nil, statusInfo, "failed 2 time(s)"},
		{"a week of errors warns", ptr(releaseRecord(now, func(s *update.State) {
			s.CheckError = &update.CheckError{Kind: update.CheckErrorUnavailable, Message: "proxy refused", Since: now.Add(-8 * 24 * time.Hour), Count: 30}
		})), false, nil, statusWarn, "HTTPS_PROXY"},
		{"never checked yet", ptr(releaseRecord(now, func(s *update.State) { s.CheckedAt = time.Time{} })), false, nil, statusInfo, "no release check recorded yet"},
		{"corrupt record", nil, true, nil, statusWarn, "cannot read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.state != nil {
				require.NoError(t, update.SaveState(dir, *tt.state))
			}
			if tt.corrupt {
				require.NoError(t, os.MkdirAll(update.StateDir(dir), 0o700))
				require.NoError(t, os.WriteFile(update.StatePath(dir), []byte("{bad"), 0o600))
			}
			cfg := config.Defaults()
			cfg.DataDir = dir
			cfg.Update.Check = tt.check
			got := updatesCheck(context.Background(), cfg, nil, now)
			require.Equal(t, "updates", got.name)
			require.Equal(t, tt.wantStatus, got.status, got.detail)
			require.Contains(t, got.detail, tt.want)
		})
	}
}

// TestUpdatesCheck_ConsoleOverrideIsRead: the doctor row merges the console's
// stored override exactly as the daemon does.
func TestUpdatesCheck_ConsoleOverrideIsRead(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	require.NoError(t, update.SaveState(dir, releaseRecord(now, nil)))
	db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	off := false
	require.NoError(t, store.SetUpdateOverride(context.Background(), db, config.UpdateOverride{Check: &off}))

	cfg := config.Defaults()
	cfg.DataDir = dir
	got := updatesCheck(context.Background(), cfg, db, now)
	require.Equal(t, statusInfo, got.status)
	require.Contains(t, got.detail, "turned off in the console")
}

func TestUpdateCheckRows(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	require.NoError(t, update.SaveState(dir, releaseRecord(now, nil)))
	cfg := config.Defaults()
	cfg.DataDir = dir
	v := loadUpdateView(context.Background(), cfg, nil)

	var buf bytes.Buffer
	updateCheckRows(&buf, v, now)
	out := buf.String()
	require.Contains(t, out, "notify")
	require.Contains(t, out, "2h ago by the daemon; next in 4h")
}

func TestUpdateCheckRows_ClientHasNoRecord(t *testing.T) {
	cfg := config.Defaults()
	cfg.Role = "client"
	cfg.AdvertisedURL = "https://server.example:8081"
	cfg.DataDir = t.TempDir()
	v := loadUpdateView(context.Background(), cfg, nil)
	require.Equal(t, update.KindSource, v.install.Kind, "a test binary is a source build")

	var buf bytes.Buffer
	updateCheckRows(&buf, v, time.Now())
	require.Contains(t, buf.String(), "never by the daemon")
}

func TestHumanDuration(t *testing.T) {
	require.Equal(t, "45s", humanDuration(45*time.Second))
	require.Equal(t, "12m", humanDuration(12*time.Minute))
	require.Equal(t, "47h", humanDuration(47*time.Hour))
	require.Equal(t, "3d", humanDuration(80*time.Hour))
}

func ptr[T any](v T) *T { return &v }
