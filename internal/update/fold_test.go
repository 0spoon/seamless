package update

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// record is an attempt from 0.7.2 to 0.7.3 in stage s, finished or not.
func record(why string, s Stage, finished, ok, rolledBack bool) Attempt {
	a := Attempt{
		ID: "01K7A0000000000000000000A1", From: ver("0.7.2"), To: ver("0.7.3"), Why: why,
		StartedAt: attemptNow.Add(-10 * time.Minute), HeartbeatAt: attemptNow.Add(-9 * time.Minute),
		Stage: s, OK: ok, RolledBack: rolledBack,
	}
	if finished {
		a.FinishedAt = attemptNow.Add(-9 * time.Minute)
	}
	return a
}

// TestClassify covers every outcome the updater records (task 2.08's
// contract) and every way an updater can stop without recording one.
func TestClassify(t *testing.T) {
	from, to, other := ver("0.7.2"), ver("0.7.3"), ver("0.7.9")
	tests := []struct {
		name    string
		a       Attempt
		running Version
		want    string
	}{
		// Finished records: the fields decide, whatever runs.
		{"applied", record(WhyAuto, StageDone, true, true, false), to, OutcomeApplied},
		{"applied, seen from a daemon that is somehow From", record(WhyAuto, StageDone, true, true, false), from, OutcomeApplied},
		{"rolled back", record(WhyAuto, StageRollback, true, false, true), from, OutcomeRolledBack},
		{"the rollback failed", record(WhyAuto, StageRollback, true, false, false), to, OutcomeBroken},
		{"lock failed", record(WhyAuto, StageLock, true, false, false), from, OutcomeFailed},
		{"gates refused", record(WhyAuto, StageGates, true, false, false), from, OutcomeFailed},
		{"fetch failed (network)", record(WhyAuto, StageFetch, true, false, false), from, OutcomeFailed},
		{"verify failed (signature, pin, yank)", record(WhyAuto, StageVerify, true, false, false), from, OutcomeFailed},
		{"preflight failed", record(WhyAuto, StagePreflight, true, false, false), from, OutcomeFailed},
		{"backup failed", record(WhyAuto, StageBackup, true, false, false), from, OutcomeFailed},
		{"installer failed, From intact", record(WhyAuto, StageInstall, true, false, false), from, OutcomeFailed},
		{"finished in confirm without a rollback", record(WhyAuto, StageConfirm, true, false, false), to, OutcomeBroken},
		{"done without ok", record(WhyAuto, StageDone, true, false, false), to, OutcomeBroken},
		{"a stage a newer release added", record(WhyAuto, Stage("quiesce"), true, false, false), from, OutcomeBroken},

		// Unfinished and not live: the updater stopped. What runs decides.
		{"killed before the swap", record(WhyAuto, StageFetch, false, false, false), from, OutcomeInterrupted},
		{"killed after the swap, To runs", record(WhyAuto, StageConfirm, false, false, false), to, OutcomeUnverified},
		{"killed mid-install, From runs (To may be on disk)", record(WhyAuto, StageInstall, false, false, false), from, OutcomeInterrupted},
		{"killed mid-rollback, From runs", record(WhyAuto, StageRollback, false, false, false), from, OutcomeRolledBack},
		{"killed mid-rollback, To runs", record(WhyAuto, StageRollback, false, false, false), to, OutcomeBroken},
		{"the install moved on some other way", record(WhyAuto, StageFetch, false, false, false), other, OutcomeSuperseded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Classify(tt.a, tt.running))
		})
	}
}

// A gate refusal's word is display only: an attempt.json written before
// refusals had a word decodes and folds exactly as it always did, and the
// same attempt with a word -- known or a newer updater's -- folds to the same
// state, the word kept as is on the last attempt.
func TestFold_RefusalChangesNothingButTheLastAttempt(t *testing.T) {
	const old = `{"id":"01K7A0000000000000000000A1","from":"0.7.2","to":"0.7.3","why":"auto",` +
		`"started_at":"2026-10-09T11:50:00Z","heartbeat_at":"2026-10-09T11:51:00Z","finished_at":"2026-10-09T11:51:00Z",` +
		`"ok":false,"rolled_back":false,"stage":"gates","error":"gates: the installed seamlessd is not v0.7.2"}`
	fold := func(t *testing.T, body string) State {
		t.Helper()
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(StateDir(dir), 0o700))
		require.NoError(t, os.WriteFile(AttemptPath(dir), []byte(body), 0o600))
		a, err := ReadAttempt(dir)
		require.NoError(t, err)
		var st State
		outcome := Classify(a, a.From)
		require.True(t, applyOutcome(&st, resultOf(a, outcome, attemptNow), attemptNow, true))
		return st
	}

	before := fold(t, old)
	require.Equal(t, &AttemptResult{
		ID: "01K7A0000000000000000000A1", From: ver("0.7.2"), To: ver("0.7.3"), Why: WhyAuto,
		Outcome: OutcomeFailed, Stage: StageGates, Error: "gates: the installed seamlessd is not v0.7.2",
		StartedAt: attemptNow.Add(-10 * time.Minute), FinishedAt: attemptNow.Add(-9 * time.Minute), FoldedAt: attemptNow,
	}, before.LastAttempt)
	require.Empty(t, before.LastAttempt.Refused())
	require.Equal(t, &Backoff{Until: attemptNow.Add(time.Hour), Count: 1, Version: ver("0.7.3"), Reason: OutcomeFailed}, before.Backoff,
		"a failure before anything changed backs off")
	require.Empty(t, before.Blocks)
	require.Nil(t, before.Paused)

	for word, refused := range map[string]string{RefusalStaleBinary: RefusalStaleBinary, "disk_full": ""} {
		t.Run(word, func(t *testing.T) {
			with := fold(t, strings.TrimSuffix(old, "}")+`,"refusal":"`+word+`"}`)
			require.Equal(t, word, with.LastAttempt.Refusal, "kept as is")
			require.Equal(t, refused, with.LastAttempt.Refused(), "a word this release does not know reads as a plain failure")
			with.LastAttempt.Refusal = ""
			require.Equal(t, before, with, "the word changes nothing the fold decides")
		})
	}
}

func TestAttemptResult_Refused(t *testing.T) {
	for name, tc := range map[string]struct {
		res  AttemptResult
		want string
	}{
		"a known refusal on a failure": {AttemptResult{Outcome: OutcomeFailed, Stage: StageGates, Refusal: RefusalSelfCheck}, RefusalSelfCheck},
		"none":                         {AttemptResult{Outcome: OutcomeFailed, Stage: StageGates}, ""},
		"a newer updater's word":       {AttemptResult{Outcome: OutcomeFailed, Stage: StageGates, Refusal: "disk_full"}, ""},
		"on an outcome that is not a failure": {
			AttemptResult{Outcome: OutcomeApplied, Stage: StageDone, Refusal: RefusalStaleBinary}, ""},
	} {
		require.Equal(t, tc.want, tc.res.Refused(), name)
	}
}

// folded is an AttemptResult as the fold makes it, for applyOutcome.
func folded(why, to, outcome string, s Stage) AttemptResult {
	return AttemptResult{ID: "01K7A0000000000000000000A1", From: ver("0.7.2"), To: ver(to), Why: why, Outcome: outcome, Stage: s,
		FoldedAt: attemptNow}
}

func TestApplyOutcome_RollbacksBlockAndTwoInARowPause(t *testing.T) {
	var st State
	require.True(t, applyOutcome(&st, folded(WhyAuto, "0.7.3", OutcomeRolledBack, StageRollback), attemptNow, true))
	require.Equal(t, []Block{{Version: ver("0.7.3"), Reason: BlockRolledBack, Attempt: "01K7A0000000000000000000A1", At: attemptNow}}, st.Blocks)
	require.Equal(t, []Version{ver("0.7.3")}, st.Rollbacks)
	require.Nil(t, st.Paused, "one rollback is a bad release, not a bad machine")
	require.Nil(t, st.Backoff, "a block moves the target on; no backoff")

	// The same release again (Update now, say) is not a second tag.
	applyOutcome(&st, folded(WhyAuto, "0.7.3", OutcomeRolledBack, StageRollback), attemptNow, true)
	require.Len(t, st.Rollbacks, 1)
	require.Nil(t, st.Paused)

	later := attemptNow.Add(time.Hour)
	applyOutcome(&st, folded(WhyAuto, "0.7.4", OutcomeRolledBack, StageRollback), later, true)
	require.NotNil(t, st.Paused)
	require.Equal(t, PauseRollbacks, st.Paused.Reason)
	require.Equal(t, []Version{ver("0.7.3"), ver("0.7.4")}, st.Paused.Versions)
	require.Equal(t, later, st.Paused.At)
	require.Len(t, st.Blocks, 2)

	// A success clears the count and the pause; the blocks stay.
	applyOutcome(&st, folded(WhyNow, "0.7.5", OutcomeApplied, StageDone), later, true)
	require.Nil(t, st.Paused)
	require.Nil(t, st.Rollbacks)
	require.Len(t, st.Blocks, 2)
}

func TestApplyOutcome_UpdateNowRollbackBlocksWithoutCounting(t *testing.T) {
	var st State
	applyOutcome(&st, folded(WhyAuto, "0.7.3", OutcomeRolledBack, StageRollback), attemptNow, true)
	applyOutcome(&st, folded(WhyNow, "0.7.4", OutcomeRolledBack, StageRollback), attemptNow, true)
	require.Len(t, st.Blocks, 2)
	require.Equal(t, []Version{ver("0.7.3")}, st.Rollbacks, "an attended Update now does not count toward the pause")
	require.Nil(t, st.Paused)
}

func TestApplyOutcome_VerifyBlocks(t *testing.T) {
	var st State
	require.True(t, applyOutcome(&st, folded(WhyAuto, "0.7.3", OutcomeFailed, StageVerify), attemptNow, true))
	require.Equal(t, BlockVerify, st.Blocks[0].Reason)
	require.Nil(t, st.Backoff)
	require.Nil(t, st.Rollbacks)
}

func TestApplyOutcome_PreSwapFailuresBackOff(t *testing.T) {
	var st State
	var waits []time.Duration
	for range 6 {
		applyOutcome(&st, folded(WhyAuto, "0.7.3", OutcomeFailed, StageFetch), attemptNow, true)
		waits = append(waits, st.Backoff.Until.Sub(attemptNow))
	}
	require.Equal(t, []time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 16 * time.Hour, 24 * time.Hour}, waits)
	require.Empty(t, st.Blocks, "a pre-swap failure never blocks")
	require.Equal(t, OutcomeFailed, st.Backoff.Reason)

	// A new target starts the count over.
	applyOutcome(&st, folded(WhyAuto, "0.7.4", OutcomeFailed, StageSpawn), attemptNow, true)
	require.Equal(t, time.Hour, st.Backoff.Until.Sub(attemptNow))
	require.Equal(t, 1, st.Backoff.Count)

	// A success clears it.
	applyOutcome(&st, folded(WhyAuto, "0.7.4", OutcomeApplied, StageDone), attemptNow, true)
	require.Nil(t, st.Backoff)
}

func TestApplyOutcome_InstallerFailuresBlockAtThree(t *testing.T) {
	var st State
	for i := 1; i < installFailuresToBlock; i++ {
		applyOutcome(&st, folded(WhyAuto, "0.7.3", OutcomeFailed, StageInstall), attemptNow, true)
		require.Empty(t, st.Blocks, "failure %d", i)
		require.Equal(t, i, st.InstallFailures.Count)
	}
	// Another release in between starts the count over.
	applyOutcome(&st, folded(WhyAuto, "0.7.4", OutcomeFailed, StageInstall), attemptNow, true)
	require.Equal(t, 1, st.InstallFailures.Count)
	for range installFailuresToBlock - 1 {
		applyOutcome(&st, folded(WhyAuto, "0.7.4", OutcomeFailed, StageInstall), attemptNow, true)
	}
	require.Equal(t, []Version{ver("0.7.4")}, []Version{st.Blocks[0].Version})
	require.Equal(t, BlockInstall, st.Blocks[0].Reason)
	require.Nil(t, st.InstallFailures)
	require.NotNil(t, st.Backoff, "each failure also backs off")
}

func TestApplyOutcome_BrokenBlocksAndPauses(t *testing.T) {
	var st State
	applyOutcome(&st, folded(WhyAuto, "0.7.3", OutcomeRolledBack, StageRollback), attemptNow, true)
	require.True(t, applyOutcome(&st, folded(WhyAuto, "0.7.4", OutcomeBroken, StageRollback), attemptNow, true))
	require.Equal(t, BlockBroken, st.Blocks[1].Reason)
	require.Equal(t, &Pause{Reason: PauseBroken, Versions: []Version{ver("0.7.4")}, At: attemptNow}, st.Paused)

	// A broken install outranks rollbacks in the pause.
	applyOutcome(&st, folded(WhyAuto, "0.7.5", OutcomeRolledBack, StageRollback), attemptNow.Add(time.Hour), true)
	require.Equal(t, PauseBroken, st.Paused.Reason)
}

func TestApplyOutcome_InterruptedAndALateRecord(t *testing.T) {
	var st State
	require.True(t, applyOutcome(&st, folded(WhyAuto, "0.7.3", OutcomeInterrupted, StageSpawn), attemptNow, true))
	require.Equal(t, time.Hour, st.Backoff.Until.Sub(attemptNow), "interrupted: an hour, no block")
	require.Empty(t, st.Blocks)

	// The record shows up after all: the installer failed. The interruption
	// already counted toward the backoff.
	applyOutcome(&st, folded(WhyAuto, "0.7.3", OutcomeFailed, StageFetch), attemptNow, false)
	require.Equal(t, 1, st.Backoff.Count)
	require.Equal(t, OutcomeFailed, st.LastAttempt.Outcome)

	// Or it applied after all: everything clears.
	applyOutcome(&st, folded(WhyAuto, "0.7.3", OutcomeApplied, StageDone), attemptNow, false)
	require.Nil(t, st.Backoff)
}

func TestApplyOutcome_ManualAndSupersededChangeNothing(t *testing.T) {
	for _, res := range []AttemptResult{
		folded(WhyManual, "0.7.3", OutcomeRolledBack, StageRollback),
		folded(WhyManual, "0.7.3", OutcomeBroken, StageRollback),
		folded(WhyManual, "0.7.3", OutcomeFailed, StageFetch),
		folded(WhyManual, "0.7.3", OutcomeInterrupted, StageInstall),
		folded("cron", "0.7.3", OutcomeRolledBack, StageRollback), // a why from a newer release
		folded(WhyAuto, "0.7.3", OutcomeSuperseded, StageSpawn),
	} {
		t.Run(res.Why+"/"+res.Outcome, func(t *testing.T) {
			st := State{Backoff: &Backoff{Until: attemptNow, Count: 2, Version: ver("0.7.3")}, Rollbacks: []Version{ver("0.7.1")}}
			require.False(t, applyOutcome(&st, res, attemptNow, true))
			require.Equal(t, res, *st.LastAttempt, "shown as the last attempt")
			require.Empty(t, st.Blocks)
			require.Nil(t, st.Paused)
			require.Equal(t, 2, st.Backoff.Count)
			require.Len(t, st.Rollbacks, 1)
		})
	}
}

func TestState_BlocksAreBounded(t *testing.T) {
	var st State
	for i := range maxBlocks + 5 {
		st.block(Version{0, 8, i}, BlockVerify, "", attemptNow)
	}
	require.Len(t, st.Blocks, maxBlocks)
	require.Equal(t, Version{0, 8, 5}, st.Blocks[0].Version, "the oldest go first")
	st.block(Version{0, 8, 10}, BlockRolledBack, "x", attemptNow)
	require.Len(t, st.Blocks, maxBlocks, "a refreshed block is not a second one")
	require.Equal(t, BlockRolledBack, blocked(st.Blocks, Version{0, 8, 10}).Reason)
}

func TestState_Tidy(t *testing.T) {
	cur := ver("0.7.4")
	st := State{
		Pending:         &Pending{Running: ver("0.7.3"), Since: attemptNow},
		Hold:            &Hold{Through: ver("0.7.4"), From: ver("0.7.4")},
		InstallFailures: &InstallFailures{Version: ver("0.7.4"), Count: 2},
		Blocks:          []Block{{Version: ver("0.7.3")}, {Version: ver("0.7.5")}},
		Spawn:           &Spawn{ID: "x", From: ver("0.7.3"), To: cur},
	}
	// While a spawn is unsettled, only the deadline (tied to the old version)
	// goes: the new version may yet roll back.
	require.True(t, st.tidy(cur))
	require.Nil(t, st.Pending)
	require.NotNil(t, st.Hold)
	require.NotNil(t, st.InstallFailures)

	st.Spawn = nil
	require.True(t, st.tidy(cur))
	require.Nil(t, st.Hold, "running the held version lifts the hold")
	require.Nil(t, st.InstallFailures)
	require.Len(t, st.Blocks, 2, "blocks stay: one at or below the running version is inert")
	require.False(t, st.tidy(cur))
}
