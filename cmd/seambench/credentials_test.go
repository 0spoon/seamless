package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCredentialMode(t *testing.T) {
	for _, s := range []string{"auto", "env", "keychain", "none", " keychain "} {
		m, err := parseCredentialMode(s)
		require.NoError(t, err, s)
		require.NotEmpty(t, m)
	}
	_, err := parseCredentialMode("kechain")
	require.ErrorContains(t, err, "auto, env, keychain, none")
}

func TestResolveCredentialMode_EnvKeyWinsOverKeychain(t *testing.T) {
	// An exported key needs no file on disk, so auto must prefer it.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-not-a-real-key")
	mode, why, err := resolveCredentialMode(credAuto)
	require.NoError(t, err)
	require.Equal(t, credEnv, mode)
	require.Contains(t, why, "ANTHROPIC_API_KEY")
	require.NotContains(t, why, "sk-test-not-a-real-key", "the reason line must never carry the value")
}

func TestResolveCredentialMode_ExplicitIsUntouched(t *testing.T) {
	for _, m := range []credentialMode{credEnv, credKeychain, credNone} {
		got, why, err := resolveCredentialMode(m)
		require.NoError(t, err)
		require.Equal(t, m, got)
		require.Empty(t, why)
	}
}

func TestEnvCredentials_OnlySetNonEmpty(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "key-1")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok-1")
	require.Equal(t, []string{"ANTHROPIC_API_KEY=key-1", "CLAUDE_CODE_OAUTH_TOKEN=tok-1"}, envCredentials())
	require.Equal(t, []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}, envCredentialNames())
}

func TestCredentials_EnvModeWritesNothing(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "key-1")
	c := &credentials{mode: credEnv}
	a := &arm{configDir: t.TempDir()}

	release, err := c.provision(a)
	require.NoError(t, err)
	defer release()

	require.Equal(t, []string{"ANTHROPIC_API_KEY=key-1"}, c.env())
	require.NoFileExists(t, filepath.Join(a.configDir, credentialFile))
}

func TestCredentials_NoneProvisionsNothing(t *testing.T) {
	c := &credentials{mode: credNone}
	a := &arm{configDir: t.TempDir()}

	release, err := c.provision(a)
	require.NoError(t, err)
	defer release()

	require.Nil(t, c.env())
	require.NoFileExists(t, filepath.Join(a.configDir, credentialFile))
}

// The keychain path puts a real credential on disk in a throwaway dir, so the
// two rules that make that acceptable are pinned here: the window is one run,
// and only the login travels.
func TestCredentials_KeychainWritesForOneRunThenRemoves(t *testing.T) {
	login := []byte(`{"claudeAiOauth":{"accessToken":"tok"}}`)
	c := &credentials{mode: credKeychain, login: login}
	a := &arm{configDir: filepath.Join(t.TempDir(), "cfg")}
	path := filepath.Join(a.configDir, credentialFile)

	release, err := c.provision(a)
	require.NoError(t, err)
	require.FileExists(t, path)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, login, got)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a credential must not be world-readable")

	release()
	require.NoFileExists(t, path, "the credential must not outlive the run")

	release() // cleanup is always safe to call again
	require.NoFileExists(t, path)
}

func TestCredentials_KeychainSkipsAnArmWithNoConfigDir(t *testing.T) {
	c := &credentials{mode: credKeychain, login: []byte(`{"claudeAiOauth":{}}`)}
	release, err := c.provision(&arm{})
	require.NoError(t, err)
	release()
}

// narrowLogin is the extraction readKeychainLogin performs, exercised directly:
// the real call shells out to `security` and needs a logged-in macOS keychain,
// but the rule it enforces is what matters and must hold without one.
func TestKeychainLogin_DropsThirdPartyMCPSecrets(t *testing.T) {
	// The shape the real Keychain item has: the Claude login alongside OAuth
	// credentials for unrelated MCP servers, client secrets included.
	blob := `{
	  "claudeAiOauth": {"accessToken":"claude-tok","refreshToken":"claude-refresh","subscriptionType":"max"},
	  "mcpOAuth": {
	    "plugin:figma:figma|abc123": {
	      "accessToken":"figma-tok","refreshToken":"figma-refresh","clientSecret":"figma-secret"
	    }
	  }
	}`
	narrowed, err := narrowLogin([]byte(blob))
	require.NoError(t, err)

	s := string(narrowed)
	require.Contains(t, s, "claude-tok", "the Claude login must travel")
	for _, secret := range []string{"figma-tok", "figma-refresh", "figma-secret", "mcpOAuth"} {
		require.NotContains(t, s, secret,
			"a third-party MCP secret must never be copied into an arm")
	}

	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(narrowed, &doc))
	require.Equal(t, []string{"claudeAiOauth"}, keysOf(doc))
}

func TestKeychainLogin_RejectsUnusableItems(t *testing.T) {
	_, err := narrowLogin([]byte("not json"))
	require.ErrorContains(t, err, "not JSON")

	_, err = narrowLogin([]byte(`{"mcpOAuth":{}}`))
	require.ErrorContains(t, err, "no claudeAiOauth")
}

// A malformed item must not be echoed: the payload is a credential store.
func TestKeychainLogin_ErrorNeverQuotesThePayload(t *testing.T) {
	_, err := narrowLogin([]byte(`{"claudeAiOauth" oops "accessToken":"super-secret-token"}`))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "super-secret-token")
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
