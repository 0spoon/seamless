package update

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The shared words, pinned: the briefing, the console and the CLI all render
// these strings, so a change here is a change on every surface.
func TestBlockWords(t *testing.T) {
	for reason, want := range map[string]string{
		BlockRolledBack: "the update to it rolled back",
		BlockVerify:     "it did not pass verification",
		BlockInstall:    "its installer failed 3 times in a row",
		BlockBroken:     "the update to it could not be rolled back cleanly",
		"a_newer_word":  "an update to it failed",
		"":              "an update to it failed",
	} {
		require.Equal(t, want, BlockWords(reason), reason)
	}
}

func TestPauseWords(t *testing.T) {
	require.Equal(t, "two updates in a row rolled back", PauseWords(PauseRollbacks))
	require.Equal(t, "an update could not be rolled back cleanly", PauseWords(PauseBroken))
	require.Equal(t, "two updates in a row rolled back", PauseWords("a_newer_word"))
}

func TestSpanWords(t *testing.T) {
	require.Equal(t, "24h", SpanWords(24*time.Hour))
	require.Equal(t, "90m", SpanWords(90*time.Minute))
	require.Equal(t, "90s", SpanWords(90*time.Second))
	require.Equal(t, "1.5s", SpanWords(1500*time.Millisecond))
	require.Equal(t, "1m30.5s", SpanWords(90*time.Second+500*time.Millisecond))
}

func TestPlural(t *testing.T) {
	require.Equal(t, "1 failure", Plural(1, "failure"))
	require.Equal(t, "0 failures", Plural(0, "failure"))
	require.Equal(t, "3 failures", Plural(3, "failure"))
}

// TestHeldBack: the skips, soak aside, in the order they win, each with the
// words a surface sets after "automatic updates skip it: ".
func TestHeldBack(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	r := rel("0.7.4", now.Add(-time.Minute), false) // unsigned, and not soaked on any clock
	hold := &Hold{Through: ver("0.7.4"), From: ver("0.7.3")}
	blocks := []Block{{Version: ver("0.7.4"), Reason: BlockVerify}}

	code, why := HeldBack(r, blocks, hold)
	require.Equal(t, WaitHeld, code, "a hold outranks a block")
	require.Equal(t, "this install went back from v0.7.3, so automatic updates skip releases up to v0.7.4 until they are resumed", why)

	code, why = HeldBack(r, blocks, &Hold{Through: ver("0.7.3"), From: ver("0.7.3")})
	require.Equal(t, WaitBlocked, code, "a hold below r does not reach it; the block outranks the missing bundle")
	require.Equal(t, BlockWords(BlockVerify), why)

	code, why = HeldBack(r, []Block{{Version: ver("0.7.5"), Reason: BlockVerify}}, nil)
	require.Equal(t, WaitUnsigned, code)
	require.Equal(t, "it carries no signed checksums bundle, which an automatic update verifies", why)

	r.ChecksumsBundle = true
	code, why = HeldBack(r, nil, nil)
	require.Empty(t, code, "a soak is not HeldBack's to judge")
	require.Empty(t, why)
}

// TestBlockWords_InstallCountsInstallerFailuresOnly is why the CLI's old
// wording for BlockInstall, "its installer failed on every try", was wrong and
// BlockWords' count is right: a try that stops before the installer runs (a
// failed download, say) neither counts toward the block nor resets it, so a
// release is blocked after tries that did not all fail at the installer.
func TestBlockWords_InstallCountsInstallerFailuresOnly(t *testing.T) {
	var st State
	applyOutcome(&st, folded(WhyAuto, "0.7.3", OutcomeFailed, StageInstall), attemptNow, true)
	applyOutcome(&st, folded(WhyAuto, "0.7.3", OutcomeFailed, StageFetch), attemptNow, true)
	applyOutcome(&st, folded(WhyAuto, "0.7.3", OutcomeInterrupted, StageFetch), attemptNow, true)
	for range installFailuresToBlock - 1 {
		applyOutcome(&st, folded(WhyAuto, "0.7.3", OutcomeFailed, StageInstall), attemptNow, true)
	}
	require.Len(t, st.Blocks, 1)
	require.Equal(t, BlockInstall, st.Blocks[0].Reason, "five tries, two of them never reached the installer")
	require.Equal(t, "its installer failed 3 times in a row", BlockWords(st.Blocks[0].Reason))
}
