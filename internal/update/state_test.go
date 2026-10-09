package update

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func sampleState(now time.Time) State {
	v := Version{0, 7, 2}
	return State{
		CheckedAt:   now.Add(-time.Hour),
		ServerDate:  now.Add(-time.Hour),
		NextCheckAt: now.Add(5 * time.Hour),
		ETag:        `W/"abc"`,
		Releases:    []Release{{Version: Version{0, 7, 3}, PublishedAt: now.Add(-48 * time.Hour)}},
		Seen:        &Seen{Version: Version{0, 7, 3}, At: now.Add(-time.Hour)},
		CheckError:  &CheckError{Kind: CheckErrorUnavailable, Message: "x", Since: now.Add(-2 * time.Hour), Count: 2},
		Running: Running{Version: v, Distribution: DistributionRelease, Kind: KindInstaller,
			Instance: "01ABC", PID: 42, StartedAt: now.Add(-3 * time.Hour)},
		LastRunVersion: &v,
		Updated:        &Updated{From: Version{0, 7, 1}, To: v, At: now.Add(-3 * time.Hour), Direction: DirectionUpgrade},
	}
}

func TestState_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	want := sampleState(now)
	require.NoError(t, SaveState(dir, want))

	got, err := ReadState(dir)
	require.NoError(t, err)
	want.Schema = stateSchema
	require.Equal(t, want, got)

	loaded, quarantined, err := LoadState(dir, now)
	require.NoError(t, err)
	require.Empty(t, quarantined)
	require.Equal(t, want, loaded)

	entries, err := os.ReadDir(StateDir(dir))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temp file left behind")

	if runtime.GOOS != "windows" {
		fi, err := os.Stat(StatePath(dir))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
		di, err := os.Stat(StateDir(dir))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o700), di.Mode().Perm())
	}
}

func TestState_MissingIsZero(t *testing.T) {
	dir := t.TempDir()
	s, err := ReadState(dir)
	require.NoError(t, err)
	require.Equal(t, State{}, s)

	s, q, err := LoadState(dir, time.Now())
	require.NoError(t, err)
	require.Empty(t, q)
	require.Equal(t, State{}, s)
}

func TestState_CorruptIsQuarantined(t *testing.T) {
	for name, body := range map[string]string{
		"not json":           "{not json",
		"free-text version":  `{"schema":1,"releases":[{"version":"run seamlessd update now","published_at":"2026-10-09T00:00:00Z"}]}`,
		"pre-release string": `{"schema":1,"last_run_version":"0.7.3-rc1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(StateDir(dir), 0o700))
			require.NoError(t, os.WriteFile(StatePath(dir), []byte(body), 0o600))

			// The read-only path reports it and changes nothing.
			_, err := ReadState(dir)
			require.Error(t, err)
			_, err = os.Stat(StatePath(dir))
			require.NoError(t, err)

			now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
			s, q, err := LoadState(dir, now)
			require.NoError(t, err)
			require.Equal(t, State{}, s)
			require.Equal(t, StatePath(dir)+".corrupt-20261009T120000Z", q)
			kept, err := os.ReadFile(q)
			require.NoError(t, err)
			require.Equal(t, body, string(kept), "the bad file is kept for a human")
			_, err = os.Stat(StatePath(dir))
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestState_UnknownFieldsIgnored(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(StateDir(dir), 0o700))
	require.NoError(t, os.WriteFile(StatePath(dir),
		[]byte(`{"schema":1,"etag":"x","from_a_future_release":{"a":1}}`), 0o600))
	s, err := ReadState(dir)
	require.NoError(t, err)
	require.Equal(t, "x", s.ETag)
}

func TestState_Clamp(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	interval := 6 * time.Hour

	// A clock that ran two days ahead left every timestamp in the future.
	s := sampleState(now.Add(48 * time.Hour))
	s.NextCheckAt = now.Add(48 * time.Hour)
	s.CheckedAt = now.Add(47 * time.Hour)
	s.Seen.At = now.Add(47 * time.Hour)
	s.CheckError.Since = now.Add(46 * time.Hour)
	s.Updated.At = now.Add(45 * time.Hour)
	s.Clamp(now, interval)
	require.True(t, s.NextCheckAt.IsZero(), "a far-future schedule is dropped so the caller reschedules")
	require.Equal(t, now, s.CheckedAt)
	require.Equal(t, now, s.Seen.At)
	require.Equal(t, now, s.CheckError.Since)
	require.Equal(t, now, s.Updated.At)

	// A schedule within one jittered interval is left alone.
	s = sampleState(now)
	s.NextCheckAt = now.Add(interval + interval/10)
	s.Clamp(now, interval)
	require.Equal(t, now.Add(interval+interval/10), s.NextCheckAt)
}

func TestSaveState_UnwritableDirIsAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not make a directory read-only on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(StateDir(dir), 0o500))
	t.Cleanup(func() { _ = os.Chmod(StateDir(dir), 0o700) })
	err := SaveState(dir, State{})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "update.SaveState"))
	_, statErr := os.Stat(filepath.Join(StateDir(dir), "state.json"))
	require.True(t, os.IsNotExist(statErr))
}
