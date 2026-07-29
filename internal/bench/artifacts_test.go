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
		Scenario:  "cookie-hardening",
		Condition: Condition{Name: "mechanism", Profile: ProfileMechanism, Client: ClientClaude},
		Run:       2,
		Version:   "0203c7b",
		Model:     "claude-opus-5",
		Prompt:    "take care of the cookie hardening",
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

// A multi-step manifest carries the per-session breakdown; the top-level
// metrics stay the runner-half sum so every existing reader keeps working.
func TestRunRecord_MultiStepRoundTrip(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().UTC().Truncate(time.Second)
	steps := []StepRecord{
		{Name: "investigate", Prompt: "find the root cause", SessionID: "s1",
			StartedAt: at, EndedAt: at.Add(time.Minute), ExitCode: 0,
			Metrics: Metrics{Turns: 4, InputTokens: 700, OutputTokens: 80, CostUSD: 0.2, DurationMS: 60000}},
		{Name: "fix", Prompt: "land the fix", SessionID: "s2",
			StartedAt: at.Add(2 * time.Minute), EndedAt: at.Add(5 * time.Minute), ExitCode: 0,
			Metrics: Metrics{Turns: 9, InputTokens: 1300, OutputTokens: 220, CostUSD: 0.5, DurationMS: 180000}},
	}
	want := RunRecord{
		Scenario: "restart-logouts", Condition: DefaultConditions()[1], Run: 1,
		Metrics: SumRunnerMetrics(steps[0].Metrics, steps[1].Metrics),
		Steps:   steps,
	}
	require.NoError(t, WriteRunRecord(dir, want))

	got, err := ReadRunRecord(dir)
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, Metrics{Turns: 13, InputTokens: 2000, OutputTokens: 300, CostUSD: 0.7, DurationMS: 240000}, got.Metrics)
}

// SumRunnerMetrics sums ONLY the runner half: the grader half is derived once
// from the whole run's event log, never summed per step.
func TestSumRunnerMetrics_LeavesTheGraderHalfAlone(t *testing.T) {
	sum := SumRunnerMetrics(
		Metrics{Turns: 1, ToolCalls: 5, Injections: 1},
		Metrics{Turns: 2, ToolCalls: 7, Injections: 1},
	)
	require.Equal(t, 3, sum.Turns)
	require.Zero(t, sum.ToolCalls)
	require.Zero(t, sum.Injections)
}

// LoadRun surfaces the NON-final sessions' preserved artifacts under Steps;
// the final session's stay in the top-level fields.
func TestLoadRun_MultiStepArtifacts(t *testing.T) {
	dir := t.TempDir()
	rec := RunRecord{
		Scenario: "restart-logouts", Condition: DefaultConditions()[1], Run: 1,
		Steps: []StepRecord{{Name: "investigate", Prompt: "p1"}, {Name: "fix", Prompt: "p2"}},
	}
	require.NoError(t, WriteRunRecord(dir, rec))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, RepoDirName), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, TranscriptFile), []byte("{}\n"), 0o644))

	stepDir := filepath.Join(dir, StepDirName(1))
	require.NoError(t, os.MkdirAll(stepDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stepDir, DiffFile), []byte("--- a\n+++ b\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stepDir, TranscriptFile), []byte("{}\n"), 0o644))

	_, a, err := LoadRun(dir)
	require.NoError(t, err)
	require.Len(t, a.Steps, 1, "the final step's artifacts are the top-level ones")
	require.Equal(t, "investigate", a.Steps[0].Name)
	require.Equal(t, "--- a\n+++ b\n", a.Steps[0].RepoDiff)
	require.Equal(t, filepath.Join(stepDir, TranscriptFile), a.Steps[0].Transcript)

	t.Run("missing step artifacts are empty, not errors", func(t *testing.T) {
		require.NoError(t, os.RemoveAll(stepDir))
		_, a, err := LoadRun(dir)
		require.NoError(t, err)
		require.Len(t, a.Steps, 1)
		require.Empty(t, a.Steps[0].RepoDiff)
		require.Empty(t, a.Steps[0].Transcript)
	})
}

func TestLoadRun_OptionalArtifactsAreEmptyNotErrors(t *testing.T) {
	dir := t.TempDir()
	rec := RunRecord{Scenario: "cookie-hardening", Condition: DefaultConditions()[0], Run: 1}
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
	require.Equal(t, "cookie-hardening", a.Scenario)
	require.Equal(t, DefaultConditions()[0], a.Condition)
}

func TestLoadRun_MissingManifest(t *testing.T) {
	_, _, err := LoadRun(t.TempDir())
	require.Error(t, err)
}
