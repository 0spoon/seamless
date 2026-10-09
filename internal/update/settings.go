package update

import (
	"time"

	"github.com/arctop/seamless/internal/config"
)

// Where a setting's value came from, as the console and doctor name it.
const (
	SourceDefault = "default" // nothing set it; the build decides
	SourceConfig  = "config"  // seamless.yaml or a SEAMLESS_UPDATE_* variable
	SourceConsole = "console" // the console's stored override
)

// Settings is the update behavior in force: the file/env config with the
// console's stored override layered on (Effective).
type Settings struct {
	// Check is whether the daemon asks GitHub at all. false means no update
	// traffic of any kind.
	Check bool
	// CheckSource names who decided Check (SourceDefault, SourceConfig or
	// SourceConsole), and CheckLocked is true when the console may not turn
	// it on: the file or environment set it false.
	CheckSource string
	CheckLocked bool
	// CheckInterval is the time between scheduled checks.
	CheckInterval time.Duration
}

// Effective merges the file/env update config with the console's override.
// Unlike the briefing and features overrides, where the stored row simply
// wins, here the MORE RESTRICTIVE setting wins:
//
//   - an explicit file/env false is final: the override cannot turn it on,
//     and the console shows the toggle locked with where it was set -- so
//     "check: false means no network" is never one click from untrue;
//   - an explicit file/env true can still be turned off from the console;
//   - with the key unset the console is free either way, and with no override
//     either, a release build checks and a source build does not.
//
// release is IsReleaseBuild for the running binary.
func Effective(base config.Update, override config.UpdateOverride, release bool) Settings {
	s := Settings{CheckInterval: base.CheckInterval.Std()}
	if s.CheckInterval <= 0 {
		s.CheckInterval = config.DefaultUpdateCheckInterval
	}
	switch {
	case base.Check != nil && !*base.Check:
		s.Check, s.CheckSource, s.CheckLocked = false, SourceConfig, true
	case override.Check != nil:
		s.Check, s.CheckSource = *override.Check, SourceConsole
	case base.Check != nil:
		s.Check, s.CheckSource = true, SourceConfig
	default:
		s.Check, s.CheckSource = release, SourceDefault
	}
	return s
}
