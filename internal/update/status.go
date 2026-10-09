package update

import (
	"fmt"
	"strings"
	"time"
)

// Notice windows: how long the briefing mentions an update.
const (
	// updatedNoticeWindow is how long after a version change the briefing
	// says the daemon updated.
	updatedNoticeWindow = 24 * time.Hour
	// availableNoticeWindow is how long the briefing mentions one available
	// release, counted from when this install first saw it. After that the
	// console and doctor still show it; sessions stop hearing about it.
	availableNoticeWindow = 7 * 24 * time.Hour
)

// Mode is what the update machinery does on this install.
type Mode string

const (
	// ModeOff: no update checks at all.
	ModeOff Mode = "off"
	// ModeNotify: check and tell the owner; installing stays theirs.
	ModeNotify Mode = "notify"
)

// Status is the one snapshot of the update check that every surface renders:
// the briefing notice, the console Updates section and health strip, doctor,
// /healthz. The checker owns it; readers get a copy.
type Status struct {
	// The running build.
	Version      string    `json:"version"` // as built, e.g. "0.7.2"
	Distribution string    `json:"distribution"`
	Release      bool      `json:"release"` // IsReleaseBuild
	Instance     string    `json:"instance"`
	StartedAt    time.Time `json:"started_at"`
	Install      Install   `json:"install"`
	Settings     Settings  `json:"settings"`

	// What the last successful check saw. Newest is nil until one succeeds.
	Newest      *Release    `json:"newest,omitempty"`
	Available   bool        `json:"available"` // Newest is newer than Version
	SeenAt      time.Time   `json:"seen_at,omitzero"`
	CheckedAt   time.Time   `json:"checked_at,omitzero"`
	NextCheckAt time.Time   `json:"next_check_at,omitzero"`
	CheckError  *CheckError `json:"check_error,omitempty"`

	// Updated is the last version change observed at startup (release
	// builds only).
	Updated *Updated `json:"updated,omitempty"`

	// Stopped is true when the checker's loop died (a recovered panic); the
	// daemon keeps serving and the surfaces say the check is not running.
	Stopped bool `json:"stopped,omitempty"`
}

// Mode reports what the update machinery does here and why, in words for the
// owner. Installing is not automatic on any install yet, so every mode that
// checks is ModeNotify.
func (s Status) Mode() (Mode, string) {
	if !s.Settings.Check {
		switch {
		case s.Settings.CheckLocked:
			return ModeOff, "update checks are off (update.check: false in the config file or environment)"
		case s.Settings.CheckSource == SourceConsole:
			return ModeOff, "update checks are off (turned off in the console)"
		case !s.Release && s.Distribution == DistributionRelease:
			return ModeOff, "update checks are off: this build is not a published release (a snapshot) and does not check unless update.check is true"
		case !s.Release:
			return ModeOff, "update checks are off: builds from source do not check unless update.check is true"
		default:
			return ModeOff, "update checks are off"
		}
	}
	if s.Install.Kind == KindInstaller || s.Install.Reason == "" {
		return ModeNotify, "this install is told about new releases; installing one is your call (" + s.Install.Hint + ")"
	}
	return ModeNotify, "told about new releases, never updated unattended: " + s.Install.Reason
}

// Current returns the running version parsed, and false for a build whose
// version is not a clean release (a dev or snapshot build).
func (s Status) Current() (Version, bool) { return Parse(s.Version) }

// Notice renders the one briefing line about updates for a session on host
// ("" or localHost is the daemon's own machine), or "" when there is nothing
// to say. At most one line, first match wins:
//
//  1. the daemon updated within the last day;
//  2. a newer release is available and this install is told rather than
//     updated (every install, for now), within a week of first seeing it.
//
// The text is assembled from parsed versions, the constant release URL and
// the fixed hint for the install's kind -- never from the release API's text
// (constraint update-surfaces-render-parsed-versions-only) -- and it is
// worded as the owner's action, so an agent that reads it does not decide to
// restart the daemon under every other session.
func (s Status) Notice(host, localHost string, now time.Time) string {
	remote := host != "" && !strings.EqualFold(host, localHost)

	if u := s.Updated; u != nil && u.Direction == DirectionUpgrade {
		if age := now.Sub(u.At); age >= 0 && age < updatedNoticeWindow {
			if remote {
				return fmt.Sprintf("The Seamless server updated to v%s (from v%s) %s ago. "+
					"The Seamless client on this machine updates separately; owner action, not a task for this session.",
					u.To, u.From, compactAge(age))
			}
			return fmt.Sprintf("Seamless updated to v%s (from v%s) %s ago. Release notes: %s",
				u.To, u.From, compactAge(age), ReleaseURL(u.To))
		}
	}

	if !s.Settings.Check || !s.Available || s.Newest == nil {
		return ""
	}
	if age := now.Sub(s.SeenAt); age < 0 || age >= availableNoticeWindow {
		return ""
	}
	cur, ok := s.Current()
	if !ok {
		return "" // Available is never set for a dev build; belt and braces
	}
	if remote {
		return fmt.Sprintf("The Seamless server can update to v%s (it runs v%s). Owner action on the server, not a task for this session: %s",
			s.Newest.Version, cur, s.Install.Hint)
	}
	return fmt.Sprintf("Seamless v%s is available (running v%s). Owner action, not a task for this session: %s",
		s.Newest.Version, cur, s.Install.Hint)
}

// compactAge renders a duration the way the briefing does elsewhere: "45m",
// "5h", "3d". Anything under a minute is "1m".
func compactAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(1, int(d/time.Minute)))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}
