package hooks

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseClient_AbsentDefaultsPresentMustBeCanonical(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		present bool
		want    Client
		wantErr bool
	}{
		{name: "absent", want: ClientClaudeCode},
		{name: "canonical Claude Code", raw: "claude-code", present: true, want: ClientClaudeCode},
		{name: "canonical Codex", raw: "codex", present: true, want: ClientCodex},
		{name: "present empty", present: true, wantErr: true},
		{name: "undocumented alias", raw: "claude", present: true, wantErr: true},
		{name: "unknown", raw: "gemini", present: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseClient(tt.raw, tt.present)
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), "valid values are claude-code, codex")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// The HTTP parser is not the only boundary: callers can construct InstallOptions
// directly. Every exported profile operation must reject an invalid non-empty
// Client before touching its target file instead of selecting Claude implicitly.
func TestProgrammaticHookClientRejectsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	invalid := Client("gemini")

	_, err := Install(InstallOptions{
		Client: invalid, SettingsPath: path, BaseURL: "http://127.0.0.1:8081", APIKey: "k",
	})
	require.ErrorContains(t, err, `hooks.Install: invalid hook client "gemini"`)
	require.NoFileExists(t, path)

	_, err = InstalledStatus(InstallOptions{
		Client: invalid, SettingsPath: path, BaseURL: "http://127.0.0.1:8081",
	})
	require.ErrorContains(t, err, `hooks.InstalledStatus: invalid hook client "gemini"`)

	_, err = Uninstall(UninstallOptions{
		Client: invalid, SettingsPath: path, BaseURL: "http://127.0.0.1:8081",
	})
	require.ErrorContains(t, err, `hooks.Uninstall: invalid hook client "gemini"`)

	_, err = InstalledEvents(invalid)
	require.ErrorContains(t, err, `hooks.InstalledEvents: invalid hook client "gemini"`)
}
