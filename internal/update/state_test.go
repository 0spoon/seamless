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

// TestState_AutomaticUpdateFieldsRoundTrip pins the automatic-update fields'
// JSON names: older and newer daemons read each other's file.
func TestState_AutomaticUpdateFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	v3, v4 := Version{0, 7, 3}, Version{0, 7, 4}
	want := sampleState(now)
	want.Releases[0].ChecksumsBundle = true
	want.ReleasesRev = filterRevision
	want.Running.CanApply = true
	want.Updated.CodexHooks = true
	want.RecheckAt = now.Add(10 * time.Minute)
	want.Pending = &Pending{Running: Version{0, 7, 2}, Since: now.Add(-time.Hour)}
	want.Spawn = &Spawn{ID: "01K7A0000000000000000000A1", From: Version{0, 7, 2}, To: v3, Why: WhyAuto, Path: PathIdle,
		At: now, CodexHooks: "sha256:abc"}
	want.LastAttempt = &AttemptResult{ID: "01K7A0000000000000000000A0", From: Version{0, 7, 2}, To: v4, Why: WhyNow,
		Outcome: OutcomeRolledBack, Stage: StageRollback, RolledBack: true, Error: "x", LogPath: "/l",
		SpawnedAt: now.Add(-time.Hour), StartedAt: now.Add(-time.Hour), FinishedAt: now.Add(-50 * time.Minute), FoldedAt: now}
	want.Blocks = []Block{{Version: v4, Reason: BlockRolledBack, Attempt: "01K7A0000000000000000000A0", At: now}}
	want.Hold = &Hold{Through: Version{0, 7, 5}, From: Version{0, 7, 5}, At: now}
	want.Paused = &Pause{Reason: PauseRollbacks, Versions: []Version{v3, v4}, At: now}
	want.Backoff = &Backoff{Until: now.Add(time.Hour), Count: 1, Version: v3, Reason: OutcomeInterrupted}
	want.Rollbacks = []Version{v3, v4}
	want.InstallFailures = &InstallFailures{Version: v3, Count: 2}
	want.CodexHooks = "sha256:abc"
	require.NoError(t, SaveState(dir, want))

	got, err := ReadState(dir)
	require.NoError(t, err)
	want.Schema = stateSchema
	require.Equal(t, want, got)

	raw, err := os.ReadFile(StatePath(dir))
	require.NoError(t, err)
	for _, key := range []string{
		`"releases_rev"`, `"checksums_bundle"`, `"can_apply"`, `"codex_hooks_changed"`, `"recheck_at"`, `"pending"`,
		`"spawn"`, `"spawned_at"`, `"last_attempt"`, `"folded_at"`, `"blocks"`, `"hold"`, `"skip_through"`, `"paused"`,
		`"backoff"`, `"rollbacks"`, `"install_failures"`, `"codex_hooks"`,
	} {
		require.Contains(t, string(raw), key)
	}
	require.Equal(t, 1, stateSchema, "additions keep the schema: an older reader ignores them")
}

// The last attempt's refusal is saved under its own key, an addition that
// keeps the schema; a state file from before it reads as no refusal.
func TestState_LastAttemptRefusal(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	want := State{LastAttempt: &AttemptResult{ID: "01K7A0000000000000000000A0", From: Version{0, 7, 2}, To: Version{0, 7, 3},
		Why: WhyAuto, Outcome: OutcomeFailed, Stage: StageGates, Error: "gates: x", Refusal: RefusalStaleBinary, FoldedAt: now}}
	require.NoError(t, SaveState(dir, want))
	raw, err := os.ReadFile(StatePath(dir))
	require.NoError(t, err)
	require.Contains(t, string(raw), `"refusal": "stale_binary"`)
	got, err := ReadState(dir)
	require.NoError(t, err)
	want.Schema = stateSchema
	require.Equal(t, want, got)

	require.NoError(t, os.WriteFile(StatePath(dir), []byte(`{"schema":1,"last_attempt":{"id":"01K7A0000000000000000000A0",`+
		`"from":"0.7.2","to":"0.7.3","why":"auto","outcome":"failed","stage":"gates","error":"gates: x","folded_at":"2026-10-09T12:00:00Z"}}`), 0o600))
	got, err = ReadState(dir)
	require.NoError(t, err)
	require.Empty(t, got.LastAttempt.Refusal)
	require.Equal(t, OutcomeFailed, got.LastAttempt.Outcome)
	require.Equal(t, 1, stateSchema, "additions keep the schema: an older reader ignores them")
}

func TestState_ClampAutomaticUpdateTimes(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	ahead := now.Add(48 * time.Hour)
	s := State{
		RecheckAt:   ahead,
		Spawn:       &Spawn{ID: "x", At: ahead},
		Backoff:     &Backoff{Until: ahead.Add(48 * time.Hour)},
		LastAttempt: &AttemptResult{FoldedAt: ahead},
		Hold:        &Hold{At: ahead},
		Paused:      &Pause{At: ahead},
		Blocks:      []Block{{At: ahead}, {At: now.Add(-time.Hour)}},
		Pending:     &Pending{Since: ahead},
	}
	s.Clamp(now, 6*time.Hour)
	require.True(t, s.RecheckAt.IsZero(), "a far-future re-check is dropped")
	require.Equal(t, now, s.Spawn.At, "so its start window can pass")
	require.Equal(t, now.Add(backoffMax), s.Backoff.Until)
	require.Equal(t, now, s.LastAttempt.FoldedAt)
	require.Equal(t, now, s.Hold.At)
	require.Equal(t, now, s.Paused.At)
	require.Equal(t, []time.Time{now, now.Add(-time.Hour)}, []time.Time{s.Blocks[0].At, s.Blocks[1].At})
	require.Equal(t, ahead, s.Pending.Since, "GitHub's clock, not this one: left alone")

	// A re-check due within the post-update wait is kept.
	s.RecheckAt = now.Add(postUpdateCheckDelay + firstCheckJitter)
	s.Clamp(now, 6*time.Hour)
	require.Equal(t, now.Add(postUpdateCheckDelay+firstCheckJitter), s.RecheckAt)
}
