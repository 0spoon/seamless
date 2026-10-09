package console

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/arctop/seamless/internal/config"
	"github.com/arctop/seamless/internal/update"
)

// The console's view of the background update check: Settings > Updates
// (settings_updates.go), the Home health strip's version fact, the one-day
// "updated" banner every page carries, and the two update event kinds.
//
// Every version and link these surfaces render is a parsed update.Version or
// update.ReleaseURL -- never text from the release API (constraint
// update-surfaces-render-parsed-versions-only).

// UpdatesView is what the console needs from the background update check
// (*update.Checker in the daemon).
type UpdatesView interface {
	Status() update.Status
	CheckNow(ctx context.Context) (update.Status, error)
	Refresh(ctx context.Context) (update.Status, error)
}

// The daemon's checker is the one production UpdatesView.
var _ UpdatesView = (*update.Checker)(nil)

// updatedBannerWindow is how long after an upgrade every page says so: the
// same day the briefing's "Seamless updated" line covers (internal/update).
const updatedBannerWindow = 24 * time.Hour

// updatesSectionHref is Settings > Updates, where every update surface links.
const updatesSectionHref = "/console/settings?s=updates"

// recentUpgrade returns the version change the daemon observed at startup when
// it is an upgrade made within updatedBannerWindow of now, else nil. A
// downgrade is the owner's own act (or a rollback), never news to announce.
func recentUpgrade(st update.Status, now time.Time) *update.Updated {
	u := st.Updated
	if u == nil || u.Direction != update.DirectionUpgrade {
		return nil
	}
	if age := now.Sub(u.At); age < 0 || age >= updatedBannerWindow {
		return nil
	}
	return u
}

// updateBanner is the note every page carries for a day after the daemon
// upgraded. It is a banner rather than a toast because the tab's live stream
// is reconnecting right after the restart, and a toast would be missed.
type updateBanner struct {
	// To and From are the parsed versions ("0.7.3"; the template adds the v).
	// To also keys the per-tab dismissal, so a newer update shows again.
	To, From string
	// Ago is how long ago the new version started ("5h ago", "just now"),
	// and At the exact time, for the hover.
	Ago, At string
	// NotesURL is the release page of To (update.ReleaseURL).
	NotesURL string
}

// updateBannerAt builds the banner, or nil when this process runs no update
// check or the daemon did not upgrade within the last day.
func (s *Service) updateBannerAt(now time.Time) *updateBanner {
	if s.cfg.Updates == nil {
		return nil
	}
	u := recentUpgrade(s.cfg.Updates.Status(), now)
	if u == nil {
		return nil
	}
	return &updateBanner{
		To: u.To.String(), From: u.From.String(),
		Ago: agoPhrase(u.At), At: ts(u.At),
		NotesURL: update.ReleaseURL(u.To),
	}
}

// versionFact is the health strip's version statement. With no update check in
// this process it is today's plain build version, linked to Your setup. With
// one it links to Settings > Updates and says the one thing the owner may act
// on: a newer release while checks are on (warn), else an upgrade within the
// last day (ok), else the plain version. ok is false when there is no version
// to state at all.
func (s *Service) versionFact(now time.Time) (healthFact, bool) {
	version := strings.TrimSpace(s.cfg.Version)
	if s.cfg.Updates == nil {
		if version == "" {
			return healthFact{}, false
		}
		return healthFact{Icon: "info", Text: "version " + version, Href: "/console/settings?s=setup"}, true
	}
	st := s.cfg.Updates.Status()
	if cur, ok := st.Current(); ok && st.Settings.Check && st.Available && st.Newest != nil {
		newest := st.Newest.Version.String()
		return healthFact{
			Icon: "refresh-cw", Tone: "warn", Href: updatesSectionHref,
			Text:  "v" + cur.String() + " -- v" + newest + " available",
			Title: "Seamless v" + newest + " was published " + day(st.Newest.PublishedAt) + "; updating is your call",
		}, true
	}
	if u := recentUpgrade(st, now); u != nil {
		return healthFact{
			Icon: "refresh-cw", Tone: "ok", Href: updatesSectionHref,
			Text:  "v" + u.To.String() + " -- updated " + agoPhrase(u.At),
			Title: "Updated from v" + u.From.String() + " at " + ts(u.At),
		}, true
	}
	if version == "" {
		version = strings.TrimSpace(st.Version)
	}
	if version == "" {
		return healthFact{}, false
	}
	return healthFact{Icon: "info", Text: "version " + version, Href: updatesSectionHref}, true
}

// updateAvailableSummary is the ledger line of an update.available event,
// built from its payload's versions only once they parse. The live toast
// shows the same line.
func updateAvailableSummary(p map[string]any) string {
	v, ok := update.Parse(payloadStr(p, "version"))
	if !ok {
		return "a newer Seamless release is available"
	}
	line := "Seamless v" + v.String() + " is available"
	if cur, ok := update.Parse(payloadStr(p, "current")); ok {
		line += " (running v" + cur.String() + ")"
	}
	return line
}

// updateAppliedSummary is the ledger line of an update.applied event: an
// upgrade reads "updated to", any other change "changed to", so a rollback or
// a pinned older version is never called an update.
func updateAppliedSummary(p map[string]any) string {
	to, ok := update.Parse(payloadStr(p, "to"))
	if !ok {
		return "Seamless changed version"
	}
	verb := "changed to"
	if payloadStr(p, "direction") == update.DirectionUpgrade {
		verb = "updated to"
	}
	line := verb + " v" + to.String()
	if from, ok := update.Parse(payloadStr(p, "from")); ok {
		line += " (from v" + from.String() + ")"
	}
	return line
}

// intervalPhrase renders a check interval for "every ...": "6 hours", "hour",
// "day", "3 days", or the compact duration when it is not whole hours
// ("1h30m").
func intervalPhrase(d time.Duration) string {
	const dayLen = 24 * time.Hour
	switch {
	case d < time.Hour || d%time.Hour != 0:
		return config.Duration(d).String()
	case d%dayLen == 0:
		return countPhrase(int(d/dayLen), "day")
	default:
		return countPhrase(int(d/time.Hour), "hour")
	}
}

// countPhrase is "hour" for one and "6 hours" for more.
func countPhrase(n int, unit string) string {
	if n == 1 {
		return unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// untilPhrase is ago() looking forward: "in 22m", "in 5h", "in 3d". A time
// already reached, or under a minute away, is "within a minute": the
// checker's one-minute tick picks a due check up by then.
func untilPhrase(t, now time.Time) string {
	d := t.Sub(now)
	switch {
	case d <= time.Minute:
		return "within a minute"
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("in %dh", int(d.Hours()))
	default:
		return fmt.Sprintf("in %dd", int(d.Hours()/24))
	}
}
