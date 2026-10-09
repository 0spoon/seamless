package config

import (
	"fmt"
	"time"
)

// Update-check interval: the default, and the bounds Update.Validate enforces.
const (
	// DefaultUpdateCheckInterval is how often the daemon asks GitHub whether a
	// newer release exists.
	DefaultUpdateCheckInterval = 6 * time.Hour
	// MinUpdateCheckInterval is the shortest allowed interval. GitHub allows 60
	// anonymous API requests an hour per address, shared by every machine and
	// tool behind it, so the check stays a small, polite fraction of that.
	MinUpdateCheckInterval = time.Hour
	// MaxUpdateCheckInterval is the longest allowed interval: 30 days.
	MaxUpdateCheckInterval = 720 * time.Hour
)

// Automatic updates: the defaults for the deadline and the soak, and the
// bounds Update.Validate enforces.
const (
	// DefaultUpdateMaxDefer is how long a pending automatic update waits for
	// an idle daemon (no live agent session, nothing in flight) before it
	// takes the next lull in requests instead.
	DefaultUpdateMaxDefer = 24 * time.Hour
	// MinUpdateMaxDefer is the shortest allowed deadline. Zero is out of
	// range, never "install at once" and never "use the default".
	MinUpdateMaxDefer = time.Minute
	// MaxUpdateMaxDefer is the longest allowed deadline: 30 days.
	MaxUpdateMaxDefer = 720 * time.Hour

	// DefaultUpdateMinAge is the soak: how long a release must have been
	// published before an automatic update takes it, so a bad release can be
	// pulled first. The floor is zero, which means no soak.
	DefaultUpdateMinAge = 24 * time.Hour
	// MaxUpdateMinAge is the longest allowed soak: 30 days.
	MaxUpdateMinAge = 720 * time.Hour
)

// Update configures the background update check and automatic updates. The
// daemon asks GitHub's releases API (one anonymous request, carrying no
// identifiers) whether a newer Seamless release exists and tells the owner.
// Auto, MaxDefer and MinAge govern whether and when it also installs one by
// itself; which installs may do that at all is internal/update's call, not
// config's.
//
// The console's runtime override (UpdateOverride, stored by
// store.SetUpdateOverride) does not simply win over these values the way the
// briefing and features rows do: internal/update merges the two so the more
// restrictive setting wins, which lets an explicit file/env false lock the
// console toggle off.
type Update struct {
	// Check turns the check on or off. nil (the key unset) means the build
	// decides: on for release builds, off for builds from source. That call is
	// internal/update's, not config's -- config only carries the three states.
	// false means zero update traffic: no request to GitHub at all.
	Check *bool `yaml:"check"`
	// CheckInterval is the time between checks, within
	// [MinUpdateCheckInterval, MaxUpdateCheckInterval].
	CheckInterval Duration `yaml:"check_interval"`
	// Auto lets the daemon install a newer release by itself. nil (the key
	// unset) means on; false keeps the check and its notices, and installing
	// stays the owner's (`seamlessd update`). As with Check, config only
	// carries the three states: internal/update decides which installs may
	// update themselves, and none does while checking is off.
	Auto *bool `yaml:"auto"`
	// MaxDefer is how long a pending automatic update waits for an idle
	// daemon before it takes the next lull in requests instead, within
	// [MinUpdateMaxDefer, MaxUpdateMaxDefer].
	MaxDefer Duration `yaml:"max_defer"`
	// MinAge is the soak: how long a release must have been published before
	// an automatic update takes it, within [0, MaxUpdateMinAge]. Zero is no
	// soak. Installing by hand ignores it.
	MinAge Duration `yaml:"min_age"`
}

// Validate rejects an update block the daemon cannot honor. Every state of
// check and auto is valid; each duration must lie within its bounds:
// check_interval [MinUpdateCheckInterval, MaxUpdateCheckInterval], max_defer
// [MinUpdateMaxDefer, MaxUpdateMaxDefer], min_age [0, MaxUpdateMinAge]. A zero
// check_interval or max_defer is out of range, never "use the default": a
// default applies only when its key is absent. A zero min_age is in range: no
// soak.
func (u Update) Validate() error {
	for _, r := range []struct {
		key    string
		v      Duration
		lo, hi time.Duration
	}{
		{"check_interval", u.CheckInterval, MinUpdateCheckInterval, MaxUpdateCheckInterval},
		{"max_defer", u.MaxDefer, MinUpdateMaxDefer, MaxUpdateMaxDefer},
		{"min_age", u.MinAge, 0, MaxUpdateMinAge},
	} {
		if d := r.v.Std(); d < r.lo || d > r.hi {
			return fmt.Errorf("config: update.%s: %s is outside [%s, %s]",
				r.key, r.v, Duration(r.lo), Duration(r.hi))
		}
	}
	return nil
}

// UpdateOverride is the console's runtime override of the update settings,
// stored as JSON in the settings table (store.UpdateOverride). A nil field is
// no override: that setting follows the file/env value. Check overrides
// update.check and Auto overrides update.auto; internal/update merges each
// with its file/env value so the more restrictive setting wins.
type UpdateOverride struct {
	Check *bool `json:"check,omitempty"`
	Auto  *bool `json:"auto,omitempty"`
}
