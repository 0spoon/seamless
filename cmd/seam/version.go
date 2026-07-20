package main

// seam version -- the running daemon's version.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var versionCmd = spec("version", groupObservability, "the running daemon's version",
	noArgs(), bindNoOpts, runVersion).
	withLong(`Prints the version of the daemon this CLI is configured to talk to, in
the same form as ` + "`seamlessd version`" + `:

    seamlessd 0.3.8 (commit 6d664d2, built 2026-07-18T09:12:04Z)

seam carries no version of its own. Both binaries ship from one tag and one
commit, so a number stamped into seam could only repeat this one or contradict
it, and only the daemon can report what is actually RUNNING -- an installed CLI
sitting next to a daemon nobody restarted would otherwise report the new version
for a process still serving the old one.

The version therefore comes from the daemon's /healthz, and an unreachable daemon
is a failure rather than a fallback: there is no second source to fall back to,
and printing seam's own build here is the exact confusion this avoids.`)

// runVersion prints the running daemon's version, in seamlessd's own phrasing.
//
// /healthz reports version as buildVersion() ("0.3.8+1a2b3c4") plus commit and
// built separately, which is everything `seamlessd version` prints -- so seam
// reassembles that one line rather than inventing a second format for the same
// fact.
func runVersion(_ context.Context, e *env, _ *noOpts, _ []string) error {
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	base := mcpBase(cfg)

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(base + "/healthz")
	if err != nil {
		return fmt.Errorf("server unreachable at %s: %w", base, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var hz map[string]any
	if derr := json.NewDecoder(resp.Body).Decode(&hz); derr != nil {
		return fmt.Errorf("unreadable health response from %s: %w", base, derr)
	}

	fmt.Fprintf(e.stdout, "seamlessd %s (commit %s, built %s)\n",
		versionOf(str(hz["version"])), str(hz["commit"]), str(hz["built"]))
	return nil
}

// versionOf strips the "+commit" suffix from the daemon's buildVersion, since
// `seamlessd version` prints the bare version and the commit separately. A
// version without the suffix (an unlinked dev build) passes through unchanged.
func versionOf(s string) string {
	base, _, _ := strings.Cut(s, "+")
	return base
}
