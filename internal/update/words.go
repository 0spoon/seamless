package update

import (
	"fmt"
	"time"
)

// The fixed words of automatic updates, for every surface that renders them:
// the briefing notice and the wait reasons in this package, the console's
// Settings > Updates and its banner, and the CLI's doctor row and `seamlessd
// update --check`. A surface frames them -- capitalizes them, sets them in its
// own sentence, appends the versions -- and never restates them, so the
// surfaces cannot drift apart. Every word is fixed or a parsed version
// (constraint update-surfaces-render-parsed-versions-only).

// unsignedWords says why a release without the checksums bundle is skipped,
// after its subject ("v0.7.4 ", "it ").
const unsignedWords = "carries no signed checksums bundle, which an automatic update verifies"

// BlockWords says why automatic updates skip a blocked release (Block.Reason),
// in words that read after "automatic updates skip v0.7.4: ". A reason this
// release does not know (a newer daemon's) reads as a plain failure.
func BlockWords(reason string) string {
	switch reason {
	case BlockRolledBack:
		return "the update to it rolled back"
	case BlockVerify:
		return "it did not pass verification"
	case BlockInstall:
		return fmt.Sprintf("its installer failed %d times in a row", installFailuresToBlock)
	case BlockBroken:
		return "the update to it could not be rolled back cleanly"
	default:
		return "an update to it failed"
	}
}

// PauseWords says why automatic updates paused themselves (Pause.Reason). A
// reason this release does not know reads as the rollbacks.
func PauseWords(reason string) string {
	if reason == PauseBroken {
		return "an update could not be rolled back cleanly"
	}
	return "two updates in a row rolled back"
}

// SpanWords renders a configured duration -- update.min_age, update.max_defer
// -- for prose, in its largest whole unit: "24h", "90m", "90s"; anything finer
// is Go's own spelling ("1m30.5s").
func SpanWords(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d >= time.Second && d%time.Second == 0:
		return fmt.Sprintf("%ds", d/time.Second)
	default:
		return d.String()
	}
}

// Plural renders a count of a regular noun: "1 request", "3 requests".
func Plural(n int64, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// HeldBack says why automatic updates skip r, a release above the running
// version, soak aside: as a wait code and in words that read after "automatic
// updates skip it: ". First match wins: a hold that reaches r (WaitHeld), a
// block on r (WaitBlocked), no checksums bundle (WaitUnsigned). Both are ""
// when none of them skips r; whether r has soaked is a question for GitHub's
// clock, which only the checker keeps (Target).
func HeldBack(r Release, blocks []Block, hold *Hold) (code, why string) {
	switch b := blocked(blocks, r.Version); {
	case held(hold, r.Version):
		return WaitHeld, fmt.Sprintf("this install went back from v%s, so automatic updates skip releases up to v%s until they are resumed",
			hold.From, hold.Through)
	case b != nil:
		return WaitBlocked, BlockWords(b.Reason)
	case !r.ChecksumsBundle:
		return WaitUnsigned, "it " + unsignedWords
	}
	return "", ""
}
