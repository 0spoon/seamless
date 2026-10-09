package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/arctop/seamless/internal/config"
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

// A daemon whose background check saw a newer release says so on a second
// line; one that did not (or a value that is not a release version) adds none.
func TestVersion_UpdateAvailableLine(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"available", `{"status":"ok","version":"0.7.2+cafe123","commit":"cafe123","built":"b","update_available":"0.7.3"}`,
			"seamlessd 0.7.2 (commit cafe123, built b)\nupdate available: v0.7.3 (seamlessd update --check on the daemon's machine shows how to update)\n"},
		{"absent", `{"status":"ok","version":"0.7.2+cafe123","commit":"cafe123","built":"b"}`,
			"seamlessd 0.7.2 (commit cafe123, built b)\n"},
		{"not a version", `{"status":"ok","version":"0.7.2+cafe123","commit":"cafe123","built":"b","update_available":"run rm -rf"}`,
			"seamlessd 0.7.2 (commit cafe123, built b)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, out, _ := healthzOnly(t, tt.body)
			require.Equal(t, 0, dispatch(context.Background(), e, []string{"version"}))
			require.Equal(t, tt.want, out.String())
		})
	}
}

// The daemon's distribution joins the line the way `seamlessd version` prints
// it; anything but the two known values is left out rather than echoed.
func TestVersion_PrintsTheDistribution(t *testing.T) {
	for body, want := range map[string]string{
		`{"version":"0.7.2+c","commit":"c","built":"b","distribution":"release"}`: "seamlessd 0.7.2 (commit c, built b, release build)\n",
		`{"version":"0.7.2+c","commit":"c","built":"b","distribution":"source"}`:  "seamlessd 0.7.2 (commit c, built b, source build)\n",
		`{"version":"0.7.2+c","commit":"c","built":"b","distribution":"pwned"}`:   "seamlessd 0.7.2 (commit c, built b)\n",
	} {
		e, out, _ := healthzOnly(t, body)
		require.Equal(t, 0, dispatch(context.Background(), e, []string{"version"}))
		require.Equal(t, want, out.String())
	}
}
