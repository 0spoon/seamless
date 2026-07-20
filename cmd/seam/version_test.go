package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
)

// All three spellings reach one handler, and each prints the daemon's version in
// `seamlessd version`'s own phrasing: /healthz reports version as
// buildVersion() ("0.3.8+1a2b3c4") with commit and built alongside, so seam
// reassembles that one line instead of inventing a second format.
func TestVersion_AllThreeSpellingsPrintTheDaemonVersion(t *testing.T) {
	for _, argv := range [][]string{{"version"}, {"-v"}, {"--version"}} {
		e, out, _ := healthzOnly(t,
			`{"status":"ok","version":"9.9.9+cafe123","commit":"cafe123","built":"2026-07-18T09:12:04Z"}`)

		require.Equal(t, 0, dispatch(context.Background(), e, argv))
		require.Equal(t, "seamlessd 9.9.9 (commit cafe123, built 2026-07-18T09:12:04Z)\n", out.String())
	}
}

// seam carries no version of its own, so an unreachable daemon has no second
// source to fall back to. Reporting seam's build here would be the exact
// confusion this command avoids -- a CLI answering for a daemon it never reached
// -- so it fails instead.
func TestVersion_UnreachableDaemonFails(t *testing.T) {
	e, out, errb := stubEnv()
	e.loadConfig = func() (config.Config, error) {
		cfg := config.Defaults()
		// A port nothing is listening on: the daemon-down path, not a stub.
		cfg.Addr = "127.0.0.1:1"
		return cfg, nil
	}

	require.Equal(t, 1, dispatch(context.Background(), e, []string{"version"}))
	require.Empty(t, out.String(), "no version line may be printed for a daemon that was never reached")
	require.Contains(t, errb.String(), "server unreachable at http://127.0.0.1:1")
}

// Something answered on the port but it was not seamlessd. Same reasoning as the
// unreachable case: no version was learned, so none is printed.
func TestVersion_UnreadableHealthResponseFails(t *testing.T) {
	e, out, errb := healthzOnly(t, `not json`)

	require.Equal(t, 1, dispatch(context.Background(), e, []string{"version"}))
	require.Empty(t, out.String())
	require.Contains(t, errb.String(), "unreadable health response")
}

// versionOf strips the daemon's "+commit" suffix, since /healthz reports
// buildVersion() while `seamlessd version` prints the bare version with the
// commit alongside. An unlinked dev build carries no suffix and passes through.
func TestVersionOf(t *testing.T) {
	require.Equal(t, "0.3.8", versionOf("0.3.8+1a2b3c4"))
	require.Equal(t, "0.0.0-dev", versionOf("0.0.0-dev"))
	require.Equal(t, "", versionOf(""))
}
