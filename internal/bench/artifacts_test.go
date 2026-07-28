package bench

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunRecord_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := RunRecord{
		Scenario:  "auth-refresh",
		Condition: Condition{Name: "mechanism", Profile: ProfileMechanism, Client: ClientClaude},
		Run:       2,
		Version:   "0203c7b",
		Model:     "claude-opus-5",
		Prompt:    "continue where we left off",
		StartedAt: time.Now().UTC().Truncate(time.Second),
		EndedAt:   time.Now().UTC().Truncate(time.Second).Add(90 * time.Second),
		ExitCode:  0,
		Metrics:   Metrics{Turns: 7, InputTokens: 1200, CostUSD: 0.42, ToolCalls: 5},
	}
	require.NoError(t, WriteRunRecord(dir, want))

	got, err := ReadRunRecord(dir)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestLoadRun_OptionalArtifactsAreEmptyNotErrors(t *testing.T) {
	dir := t.TempDir()
	rec := RunRecord{Scenario: "auth-refresh", Condition: DefaultConditions()[0], Run: 1}
	require.NoError(t, WriteRunRecord(dir, rec))

	// Nothing but the manifest: a vanilla arm that produced no transcript.
	gotRec, a, err := LoadRun(dir)
	require.NoError(t, err)
	require.Equal(t, rec.Scenario, gotRec.Scenario)
	require.Equal(t, dir, a.Dir)
	require.Empty(t, a.RepoDir)
	require.Empty(t, a.DataDir)
	require.Empty(t, a.Transcript)
	require.Empty(t, a.RepoDiff)

	// A full capture resolves every path.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, RepoDirName), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, DataDirName), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, TranscriptFile), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, DiffFile), []byte("--- a\n+++ b\n"), 0o644))

	_, a, err = LoadRun(dir)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, RepoDirName), a.RepoDir)
	require.Equal(t, filepath.Join(dir, DataDirName), a.DataDir)
	require.Equal(t, filepath.Join(dir, TranscriptFile), a.Transcript)
	require.Equal(t, "--- a\n+++ b\n", a.RepoDiff)
	require.Equal(t, "auth-refresh", a.Scenario)
	require.Equal(t, DefaultConditions()[0], a.Condition)
}

func TestLoadRun_MissingManifest(t *testing.T) {
	_, _, err := LoadRun(t.TempDir())
	require.Error(t, err)
}
