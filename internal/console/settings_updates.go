package console

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/arctop/seamless/internal/config"
	"github.com/arctop/seamless/internal/store"
	"github.com/arctop/seamless/internal/update"
)

// Settings > Updates: what the background update check does on this install,
// what it last saw, and the owner's three controls -- the check switch, a
// reset to the config file, and "Check now". The section and its routes read
// and steer Config.Updates; with none (a process that runs no update check)
// the section says so and every POST answers with an error flash.

// updatesNotRunning is the flash every Updates POST answers with when this
// process runs no update check.
const updatesNotRunning = "The update check is not running in this process, so there is nothing to change here."

// updatesLockedText names where a locked switch was set and where to change it.
const updatesLockedText = "update.check: false is set in the config file or SEAMLESS_UPDATE_CHECK; change it there."

// updatesPanel is the Updates section's view of the update check's snapshot.
// Every version and link in it comes from parsed update.Version values and
// update.ReleaseURL; the one free text it shows is the check's own last error,
// which this daemon wrote.
type updatesPanel struct {
	// Running is false when this process runs no update check: the section
	// says so and offers nothing else.
	Running bool
	// Stopped reports that the checker's loop died after an internal error.
	Stopped bool

	// Checking is the effective update.check. Mode and ModeReason are
	// Status.Mode() in words ("Notify" or "Off", and why).
	Checking   bool
	Mode       string
	ModeReason string
	// Interval is the time between scheduled checks, for "every ..." ("6
	// hours").
	Interval string
	// Source names who decided Checking (update.SourceDefault, SourceConfig
	// or SourceConsole), with the owner-facing label and note for it. Locked
	// marks an explicit file/env false the console may not turn on.
	Source      string
	SourceLabel string
	SourceNote  string
	Locked      bool

	// Version is the running build: "v0.7.2", or the build's own version
	// string when it is not a published release ("0.0.0-dev"). Build says
	// which kind of build it is ("release build", "built from source",
	// "snapshot build"), InstallReason why this install is told about
	// updates rather than updated (empty when the mode line already says
	// it), and Hint the command the owner runs to update.
	Version       string
	Build         string
	InstallReason string
	Hint          string

	// Newest is the newest installable release the last successful check saw
	// ("v0.7.3"; empty before one has), with its publication time and release
	// page. Available marks it newer than the running build; Comparable is
	// false for a build that is not a published release, which is never
	// compared.
	Newest          string
	NewestPublished time.Time
	NewestURL       string
	Available       bool
	Comparable      bool

	// CheckedAt is the last successful check; NextCheck when the next
	// scheduled one runs ("in 5h"), set only while checks are on and the
	// checker runs.
	CheckedAt   time.Time
	NextCheck   string
	NextCheckAt time.Time
	// Error is the current streak of failed checks, nil when the last check
	// worked.
	Error *updatesError
	// CheckNowBlocked says why "Check now" cannot run (checks are off, or the
	// checker stopped); empty when it can.
	CheckNowBlocked string
	// LockedNote says where a locked switch was set and where to change it
	// (the same sentence the refused save flashes); empty when not locked.
	LockedNote string
}

// updatesError is a streak of failed checks, in words.
type updatesError struct {
	// Line says how many checks failed, since when, and why ("The last 3
	// checks failed, the first 2h ago (rate limited by GitHub)").
	Line    string
	Since   time.Time
	Message string
}

// updatesPanelAt builds the Updates section from the checker's snapshot.
func (s *Service) updatesPanelAt(now time.Time) updatesPanel {
	if s.cfg.Updates == nil {
		return updatesPanel{}
	}
	st := s.cfg.Updates.Status()
	mode, modeReason := st.Mode()
	p := updatesPanel{
		Running:    true,
		Stopped:    st.Stopped,
		Checking:   st.Settings.Check,
		Mode:       "Off",
		ModeReason: modeReason,
		Interval:   intervalPhrase(st.Settings.CheckInterval),
		Source:     st.Settings.CheckSource,
		Locked:     st.Settings.CheckLocked,
		Hint:       st.Install.Hint,
		CheckedAt:  st.CheckedAt,
	}
	if mode == update.ModeNotify {
		p.Mode = "Notify"
	}
	p.SourceLabel, p.SourceNote = updatesSource(st.Settings)

	cur, comparable := st.Current()
	p.Comparable = comparable
	switch {
	case comparable:
		p.Version = "v" + cur.String()
	case strings.TrimSpace(st.Version) != "":
		p.Version = strings.TrimSpace(st.Version)
	default:
		p.Version = "unknown"
	}
	switch {
	case st.Release:
		p.Build = "release build"
	case st.Distribution == update.DistributionRelease:
		// Stamped by goreleaser without a clean X.Y.Z: a snapshot, which
		// counts as a source build everywhere the stamp matters.
		p.Build = "snapshot build"
	default:
		p.Build = "built from source"
	}
	// The install's reason once: the mode line already carries it while
	// checking, and the build line says "built from source" for that kind.
	if reason := strings.TrimSpace(st.Install.Reason); !strings.Contains(modeReason, reason) && st.Install.Kind != update.KindSource {
		p.InstallReason = reason
	}

	if n := st.Newest; n != nil {
		p.Newest = "v" + n.Version.String()
		p.NewestPublished = n.PublishedAt
		p.NewestURL = update.ReleaseURL(n.Version)
		p.Available = st.Available
	}
	if p.Checking && !p.Stopped && !st.NextCheckAt.IsZero() {
		p.NextCheck, p.NextCheckAt = untilPhrase(st.NextCheckAt, now), st.NextCheckAt
	}
	if ce := st.CheckError; ce != nil {
		p.Error = &updatesError{Line: checkErrorLine(*ce), Since: ce.Since, Message: ce.Message}
	}
	switch {
	case !p.Checking:
		p.CheckNowBlocked = "Update checks are off"
	case p.Stopped:
		p.CheckNowBlocked = "The update checker is not running"
	}
	if p.Locked {
		p.LockedNote = updatesLockedText
	}
	return p
}

// updatesSource names who decided the check switch, and what that means for
// the console's own switch.
func updatesSource(set update.Settings) (label, note string) {
	switch {
	case set.CheckLocked:
		return "Set in the config file or environment",
			"update.check: false is final there, so this console cannot turn checks on."
	case set.CheckSource == update.SourceConsole:
		return "Set in this console", "It wins over the config file and the build's default until you reset it."
	case set.CheckSource == update.SourceConfig:
		return "Set in the config file or environment",
			"update.check: true is set there; you can still turn checks off here."
	default:
		return "Default for this build",
			"Release builds check for updates and builds from source do not. Switch it here, or set update.check in the config file."
	}
}

// checkErrorLine says how long a streak of failed checks has run, and why.
func checkErrorLine(ce update.CheckError) string {
	why := "GitHub could not be reached"
	if ce.Kind == update.CheckErrorRateLimited {
		why = "rate limited by GitHub"
	}
	if ce.Count <= 1 {
		return fmt.Sprintf("The last check failed %s (%s)", agoPhrase(ce.Since), why)
	}
	return fmt.Sprintf("The last %d checks failed, the first %s (%s)", ce.Count, agoPhrase(ce.Since), why)
}

// settingsUpdatesSave stores the console's update-check switch: form field
// check, "on" or "off". Anything else is an error naming the two, and "on"
// while the config file or environment says update.check: false is refused
// with nothing stored -- the file's false is final (update.Effective). The
// stored override keeps whatever else it carries (Auto). The checker then
// re-reads it, so the section the redirect lands on shows the new state.
func (s *Service) settingsUpdatesSave(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Updates == nil {
		settingsUpdatesFlash(w, r, updatesNotRunning)
		return
	}
	values, present := r.PostForm["check"]
	switch {
	case !present:
		settingsUpdatesFlash(w, r, "check is required: valid values are on, off")
		return
	case len(values) != 1:
		settingsUpdatesFlash(w, r, "check must be given exactly once: valid values are on, off")
		return
	case values[0] != "on" && values[0] != "off":
		settingsUpdatesFlash(w, r, fmt.Sprintf("check invalid %q: valid values are on, off", values[0]))
		return
	}
	on := values[0] == "on"
	if on && s.cfg.Updates.Status().Settings.CheckLocked {
		settingsUpdatesFlash(w, r, "Update checks stay off: "+updatesLockedText)
		return
	}

	ctx := r.Context()
	o, _, err := store.UpdateOverride(ctx, s.cfg.DB)
	replaced := false
	if err != nil {
		if !undecodableOverride(err) {
			s.serverError(w, r, err)
			return
		}
		// A row that does not decode cannot be kept, and refusing would leave
		// the switch stuck: the checker already ignores it, so the save
		// replaces it, and the flash says so.
		s.logger.Warn("console: replacing an unreadable update override", "error", err)
		o, replaced = config.UpdateOverride{}, true
	}
	o.Check = &on
	if err := store.SetUpdateOverride(ctx, s.cfg.DB, o); err != nil {
		s.serverError(w, r, err)
		return
	}

	msg := "Update checks turned off: this install makes no update request from now on."
	if on {
		msg = "Update checks turned on."
	}
	if replaced {
		msg += " The stored update setting was unreadable and has been replaced."
	}
	st, err := s.cfg.Updates.Refresh(ctx)
	if err != nil {
		s.updatesRefreshFailed(w, r, msg, err)
		return
	}
	if on && st.Settings.Check && !st.NextCheckAt.IsZero() {
		msg += " The next check runs " + untilPhrase(st.NextCheckAt, time.Now()) + "."
	}
	settingsUpdatesNotice(w, r, msg)
}

// undecodableOverride reports whether err is a stored update override that is
// not valid JSON for config.UpdateOverride, as opposed to a failed read.
func undecodableOverride(err error) bool {
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	return errors.As(err, &syntax) || errors.As(err, &typ)
}

// settingsUpdatesReset clears the console's update override, so the config
// file and environment (or, without either, the build) decide again.
func (s *Service) settingsUpdatesReset(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Updates == nil {
		settingsUpdatesFlash(w, r, updatesNotRunning)
		return
	}
	ctx := r.Context()
	if err := store.ClearUpdateOverride(ctx, s.cfg.DB); err != nil {
		s.serverError(w, r, err)
		return
	}
	const msg = "Update checks follow the config file again."
	st, err := s.cfg.Updates.Refresh(ctx)
	if err != nil {
		s.updatesRefreshFailed(w, r, msg, err)
		return
	}
	state := "off"
	if st.Settings.Check {
		state = "on"
	}
	settingsUpdatesNotice(w, r, "Update checks follow the config file again: they are "+state+".")
}

// updatesRefreshFailed answers a stored change the checker could not pick up
// at once; saved is the sentence that is true regardless. A stopped checker
// reads the stored row when the daemon next starts, so that is a notice;
// anything else is an error naming what went wrong.
func (s *Service) updatesRefreshFailed(w http.ResponseWriter, r *http.Request, saved string, err error) {
	if errors.Is(err, update.ErrNotRunning) {
		settingsUpdatesNotice(w, r, saved+" The update checker is not running, so the change applies when the daemon next starts.")
		return
	}
	settingsUpdatesFlash(w, r, saved+" The update checker has not picked the change up yet: "+err.Error())
}

// settingsUpdatesCheck is "Check now": one request to GitHub, right away. The
// checker refuses while checks are off and within a minute of the previous
// check; each refusal is its own clear message.
func (s *Service) settingsUpdatesCheck(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Updates == nil {
		settingsUpdatesFlash(w, r, updatesNotRunning)
		return
	}
	st, err := s.cfg.Updates.CheckNow(r.Context())
	switch {
	case errors.Is(err, update.ErrChecksOff):
		settingsUpdatesFlash(w, r, "Update checks are off, so nothing was asked. Turn them on first.")
	case errors.Is(err, update.ErrTooSoon):
		settingsUpdatesFlash(w, r, "Checked less than a minute ago. Try again in a minute.")
	case errors.Is(err, update.ErrNotRunning):
		settingsUpdatesFlash(w, r, "The update checker is not running. Restart the daemon to revive it.")
	case err != nil && r.Context().Err() != nil:
		// This request stopped waiting, not the check: it runs on in the
		// checker, which records its own result.
		settingsUpdatesFlash(w, r, "Stopped waiting for the check. It carries on; this section shows its result when it finishes.")
	case err != nil:
		settingsUpdatesFlash(w, r, "The check failed: "+err.Error())
	default:
		settingsUpdatesNotice(w, r, checkedNowNotice(st))
	}
}

// checkedNowNotice says what a successful "Check now" found, from the parsed
// versions in the status it returned.
func checkedNowNotice(st update.Status) string {
	switch {
	case st.Newest == nil:
		return "Checked just now: no installable release was found."
	case st.Available:
		return "Checked just now: v" + st.Newest.Version.String() + " is available."
	}
	if _, ok := st.Current(); !ok {
		return "Checked just now: the newest release is v" + st.Newest.Version.String() + "."
	}
	return "Checked just now: up to date."
}

func settingsUpdatesFlash(w http.ResponseWriter, r *http.Request, msg string) {
	settingsBack(w, r, "updates", "error", msg, "updates")
}

func settingsUpdatesNotice(w http.ResponseWriter, r *http.Request, msg string) {
	settingsBack(w, r, "updates", "notice", msg, "updates")
}
