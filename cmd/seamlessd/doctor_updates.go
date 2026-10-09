package main

// The CLI's read-only view of the background update check: doctor's "updates"
// row and the extra rows of `seamlessd update --check`. Neither asks the
// daemon nor writes anything. The daemon records its own view of itself in
// <data_dir>/update/state.json (internal/update), and the effective settings
// are the config file plus the console's stored override, merged the way the
// daemon merges them.

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"time"

	"github.com/arctop/seamless/internal/config"
	"github.com/arctop/seamless/internal/store"
	"github.com/arctop/seamless/internal/update"
)

// staleCheckWarnAfter is how long the release check may keep failing before
// doctor warns instead of informing: a day of errors is a laptop offline, a
// week is a proxy or a firewall the owner should hear about.
const staleCheckWarnAfter = 7 * 24 * time.Hour

// updateView is what the CLI can know about the update check on this machine.
type updateView struct {
	// release is whether the daemon's build is a published release: from its
	// own record when there is one, else this binary's stamp.
	release  bool
	settings update.Settings
	// state is the daemon's record; stateErr why it could not be read.
	state    update.State
	stateErr error
	// install is the install kind the daemon detected (from its record), or
	// the build-only verdict when it never recorded one.
	install update.Install
	// recorded reports whether a daemon has written its record.
	recorded bool
}

// loadUpdateView assembles the view. db may be nil (no database to read the
// console's override from); the override is then ignored, as the daemon does
// when its row is unreadable.
func loadUpdateView(ctx context.Context, cfg config.Config, db *sql.DB) updateView {
	v := updateView{release: update.IsReleaseBuild(distribution, version)}
	if !cfg.IsClient() {
		v.state, v.stateErr = update.ReadState(cfg.DataDir)
	}
	if r := v.state.Running; r.Instance != "" {
		v.recorded = true
		v.release = update.IsReleaseBuild(r.Distribution, r.Version.String())
		v.install = update.Install{Kind: r.Kind, Reason: r.Reason, Hint: update.Hint(r.Kind)}
	} else {
		// No record: only what the build says can be known without the
		// daemon's own process facts (Detect's remaining gates need them).
		probe := update.Probe{Distribution: distribution, Version: version, Client: cfg.IsClient()}
		v.install = update.Detect(probe)
		if v.install.Kind == update.KindUnknown && v.release {
			v.install.Reason = "the daemon has not recorded how it was installed yet"
		}
	}
	var override config.UpdateOverride
	if db != nil {
		if o, _, err := store.UpdateOverride(ctx, db); err == nil {
			override = o
		}
	}
	v.settings = update.Effective(cfg.Update, override, v.release)
	return v
}

// status is the view as an update.Status, for its Mode wording.
func (v updateView) status() update.Status {
	return update.Status{Release: v.release, Install: v.install, Settings: v.settings}
}

// newest returns the newest installable release the daemon last saw.
func (v updateView) newest() (update.Release, bool) {
	return update.Newest(v.state.Releases)
}

// updatesCheck is doctor's "updates" row.
func updatesCheck(ctx context.Context, cfg config.Config, db *sql.DB, now time.Time) check {
	const name = "updates"
	v := loadUpdateView(ctx, cfg, db)
	if v.stateErr != nil {
		return check{statusWarn, name, fmt.Sprintf("cannot read %s: %v", update.StatePath(cfg.DataDir), v.stateErr)}
	}
	mode, reason := v.status().Mode()
	if mode == update.ModeOff {
		return check{statusInfo, name, reason + "; update with: " + v.install.Hint}
	}

	st := v.state
	if ce := st.CheckError; ce != nil {
		detail := fmt.Sprintf("the release check has failed %d time(s) since %s (%s): %s",
			ce.Count, ce.Since.Local().Format("2006-01-02 15:04"), ce.Kind, ce.Message)
		if now.Sub(ce.Since) >= staleCheckWarnAfter {
			return check{statusWarn, name, detail + " -- a proxy for the daemon goes in its service environment (HTTPS_PROXY)"}
		}
		return check{statusInfo, name, detail + "; it retries " + whenText(st.NextCheckAt, now)}
	}
	if st.CheckedAt.IsZero() {
		return check{statusInfo, name, "no release check recorded yet (the daemon checks a few minutes after it starts)"}
	}

	checked := fmt.Sprintf("checked %s ago", humanDuration(now.Sub(st.CheckedAt)))
	newest, ok := v.newest()
	cur, curOK := update.Parse(st.Running.Version.String())
	if !v.recorded {
		cur, curOK = update.Parse(version)
	}
	switch {
	case !ok:
		return check{statusInfo, name, "no installable release in the newest 20 (" + checked + ")"}
	case !curOK:
		return check{statusInfo, name, fmt.Sprintf("development build; the newest release is v%s (%s)", newest.Version, checked)}
	case newest.Version.Compare(cur) > 0:
		return check{statusWarn, name, fmt.Sprintf("v%s is available (running v%s; %s) -- update with: %s",
			newest.Version, cur, checked, v.install.Hint)}
	default:
		return check{statusOK, name, fmt.Sprintf("up to date at v%s (%s; %s install)", cur, checked, v.install.Kind)}
	}
}

// updateCheckRows prints the mode and last-check rows of `update --check`.
func updateCheckRows(w io.Writer, v updateView, now time.Time) {
	mode, reason := v.status().Mode()
	fieldRowTo(w, "mode", string(mode)+dim(" -- "+reason))
	st := v.state
	switch {
	case v.stateErr != nil:
		fieldRowTo(w, "checked", yellow("unknown")+dim(" -- "+v.stateErr.Error()))
	case st.CheckError != nil:
		fieldRowTo(w, "checked", yellow(fmt.Sprintf("failing since %s", st.CheckError.Since.Local().Format("2006-01-02 15:04")))+
			dim(fmt.Sprintf(" (%d tries; %s)", st.CheckError.Count, st.CheckError.Message)))
	case st.CheckedAt.IsZero():
		fieldRowTo(w, "checked", dim("never by the daemon"))
	default:
		next := ""
		if v.settings.Check && !st.NextCheckAt.IsZero() {
			next = "; next " + whenText(st.NextCheckAt, now)
		}
		fieldRowTo(w, "checked", fmt.Sprintf("%s ago by the daemon%s", humanDuration(now.Sub(st.CheckedAt)), next))
	}
}

// whenText renders a scheduled time relative to now: "in 3h", or "now" once due.
func whenText(t, now time.Time) string {
	if t.IsZero() || !t.After(now) {
		return "now"
	}
	return "in " + humanDuration(t.Sub(now))
}

// humanDuration renders a duration compactly: "45s", "12m", "5h", "3d".
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d/time.Second)))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}
