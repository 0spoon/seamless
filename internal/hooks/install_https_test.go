package hooks

// The https transport profile: an https base URL makes every Claude Code hook a
// `seam hook <event>` command hook, because Claude Code performs an http hook
// itself with a client that cannot be told about tls.ca_file. http installs must
// keep their exact shapes, so both halves are pinned here.

import (
	"encoding/json"
	"os"
	pathpkg "path"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const httpsBase = "https://seam.example:8443"

func TestProfileForBaseURL(t *testing.T) {
	t.Run("http returns today's table untouched", func(t *testing.T) {
		for _, base := range []string{"http://127.0.0.1:8081", "http://box.lan:8081", "", "not a url"} {
			require.Equal(t, seamlessHooks, profileForBaseURL(seamlessHooks, base), "base %q", base)
			require.Equal(t, codexHooks, profileForBaseURL(codexHooks, base), "base %q", base)
		}
	})

	t.Run("https gives every spec a CLI arg", func(t *testing.T) {
		for _, base := range []string{httpsBase, "HTTPS://seam.example:8443", "https://seam.example"} {
			for _, hs := range profileForBaseURL(seamlessHooks, base) {
				require.NotEmpty(t, hs.CLIArg, "%s must be a command hook under %q", hs.Event, base)
				require.Equal(t, pathpkg.Base(hs.Endpoint), hs.CLIArg)
			}
		}
		// The Codex profile is already all command hooks: nothing changes.
		require.Equal(t, codexHooks, profileForBaseURL(codexHooks, httpsBase))
	})

	t.Run("the source table is never mutated", func(t *testing.T) {
		_ = profileForBaseURL(seamlessHooks, httpsBase)
		for _, hs := range seamlessHooks {
			if hs.Event == "UserPromptSubmit" {
				require.Empty(t, hs.CLIArg, "the package-level table is shared; the https profile must be a copy")
			}
		}
	})
}

// The https profile derives its CLI event name from the endpoint, so it must be
// the same derivation every hand-written spec already follows -- and every name
// it can produce must be one the seam CLI is pinned against (CommandHookEndpoints
// is what cmd/seam/hook_test.go compares its own table to).
func TestHookSpecCLIArgsDeriveFromEndpoints(t *testing.T) {
	for _, profile := range [][]hookSpec{seamlessHooks, codexHooks} {
		for _, hs := range profile {
			if hs.CLIArg == "" {
				continue
			}
			require.Equal(t, pathpkg.Base(hs.Endpoint), hs.CLIArg, "%s", hs.Event)
		}
	}
	endpoints := CommandHookEndpoints()
	for _, hs := range profileForBaseURL(seamlessHooks, httpsBase) {
		require.Contains(t, endpoints, hs.CLIArg,
			"%s becomes a command hook under https, so the seam CLI pin must cover it", hs.Event)
	}
}

func TestInstall_HTTPSMakesEveryClaudeCodeHookExecForm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	opts := InstallOptions{
		SettingsPath: path, BaseURL: httpsBase, APIKey: "secret-key",
		SeamBin: "/opt/bin/seam", ConfigPath: "/etc/seamless.yaml",
	}
	res, err := Install(opts)
	require.NoError(t, err)
	require.True(t, res.Changed)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(raw, &settings))
	hooksObj := settings["hooks"].(map[string]any)

	for _, hs := range seamlessHooks {
		requireCommandHook(t, hooksObj, hs.Event, pathpkg.Base(hs.Endpoint))
	}
	// UserPromptSubmit is the one that changes: it is an http hook under http.
	requireCommandHook(t, hooksObj, "UserPromptSubmit", "user-prompt-submit")
	ups := hooksObj["UserPromptSubmit"].([]any)[0].(map[string]any)
	handler := ups["hooks"].([]any)[0].(map[string]any)
	require.Equal(t, "/opt/bin/seam", handler["command"])
	args, ok := hookStringArgs(handler["args"])
	require.True(t, ok)
	require.Equal(t, []string{"hook", "user-prompt-submit", "--config", "/etc/seamless.yaml"}, args)
	require.NotContains(t, ups, "matcher")

	// No http hook survives, so the bearer key never reaches settings.json --
	// `seam hook` reads it from the 0600 config instead.
	require.NotContains(t, string(raw), "Bearer")
	require.NotContains(t, string(raw), "secret-key")
	require.NotContains(t, string(raw), `"http"`)

	// Status agrees with what was written, and a re-install changes nothing.
	status, err := InstalledStatus(opts)
	require.NoError(t, err)
	require.Equal(t, installedEvents(t, ClientClaudeCode), status.Current)
	require.Empty(t, status.Stale)

	again, err := Install(opts)
	require.NoError(t, err)
	require.False(t, again.Changed)

	// Uninstall is still the exact inverse.
	un, err := Uninstall(UninstallOptions{SettingsPath: path, BaseURL: httpsBase})
	require.NoError(t, err)
	require.True(t, un.Changed)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(after), managedMarker)
	require.NotContains(t, string(after), "user-prompt-submit")
}

// The http shapes are unchanged by the https work: UserPromptSubmit stays the
// one http hook, with the bearer header and the 5s timeout it has always had.
func TestInstall_HTTPKeepsTheUserPromptSubmitHTTPHook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	opts := InstallOptions{
		SettingsPath: path, BaseURL: "http://127.0.0.1:8081", APIKey: "secret-key",
		SeamBin: "/opt/bin/seam", ConfigPath: "/etc/seamless.yaml",
	}
	_, err := Install(opts)
	require.NoError(t, err)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(raw, &settings))
	hooksObj := settings["hooks"].(map[string]any)

	require.Equal(t, map[string]any{
		managedMarker: true,
		"hooks": []any{map[string]any{
			"type":    "http",
			"url":     "http://127.0.0.1:8081/api/hooks/user-prompt-submit",
			"timeout": float64(5),
			"headers": map[string]any{"Authorization": "Bearer secret-key"},
		}},
	}, hooksObj["UserPromptSubmit"].([]any)[0])

	// And an https install is a DIFFERENT definition of the same event, not a
	// silently compatible one: status must report it stale rather than current.
	status, err := InstalledStatus(InstallOptions{
		SettingsPath: path, BaseURL: httpsBase, APIKey: "secret-key",
		SeamBin: "/opt/bin/seam", ConfigPath: "/etc/seamless.yaml",
	})
	require.NoError(t, err)
	require.Contains(t, status.Stale, "UserPromptSubmit")
}
