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

// Update configures the background update check: the daemon asks GitHub's
// releases API (one anonymous request, carrying no identifiers) whether a newer
// Seamless release exists, and tells the owner. Nothing here installs anything.
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
}

// Validate rejects an update block the checker cannot honor. Every state of
// check is valid; check_interval must lie within [MinUpdateCheckInterval,
// MaxUpdateCheckInterval]. A zero interval is out of range, never "use the
// default": the default applies only when the key is absent.
func (u Update) Validate() error {
	if iv := u.CheckInterval.Std(); iv < MinUpdateCheckInterval || iv > MaxUpdateCheckInterval {
		return fmt.Errorf("config: update.check_interval: %s is outside [%s, %s]",
			u.CheckInterval, Duration(MinUpdateCheckInterval), Duration(MaxUpdateCheckInterval))
	}
	return nil
}

// UpdateOverride is the console's runtime override of the update settings,
// stored as JSON in the settings table (store.UpdateOverride). A nil field is
// no override: that setting follows the file/env value. Auto is carried now,
// unused until automatic updates exist, so the stored shape never has to
// change.
type UpdateOverride struct {
	Check *bool `json:"check,omitempty"`
	Auto  *bool `json:"auto,omitempty"`
}
