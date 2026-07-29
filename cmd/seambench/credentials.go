// Getting the agent authenticated inside an arm.
//
// The fixture's guarantee is that it never touches the live ~/.claude, and
// every arm therefore runs under its own throwaway CLAUDE_CONFIG_DIR. That
// guarantee is in direct tension with the agent needing credentials, because
// Claude Code resolves them PER CONFIG DIR: a fresh config dir is "Not logged
// in" even under the real HOME, and the macOS Keychain login serves only the
// default ~/.claude (memory headless-claude-auth-is-per-config-dir). Isolation
// is exactly what removes the credentials.
//
// So the crossing is made deliberately, in one place, in one direction --
// credentials flow INTO an arm, nothing flows back out:
//
//	env       pass an API key / OAuth token through the env scrub. Nothing is
//	          written to disk. The standard headless path.
//	keychain  read the login from the macOS Keychain and write it into the
//	          arm's own config dir for the duration of one run. Owner-approved
//	          on 2026-07-28 so runs can use a subscription login.
//	none      provision nothing (what every fake-agent test uses; a real agent
//	          under it fails "Not logged in").
//
// Two rules the keychain path must keep, because it is putting a real
// credential on disk in a throwaway directory:
//
//  1. Copy ONLY the claudeAiOauth key. The Keychain blob also carries mcpOAuth
//     entries for third-party MCP servers -- access tokens, refresh tokens and
//     client secrets belonging to services that have nothing to do with this
//     benchmark. Verified 2026-07-28 that claudeAiOauth alone authenticates, so
//     copying the whole blob would spread unrelated secrets for no benefit.
//  2. Remove it when the run ends. The window is one run, not the life of the
//     fixture, and the credential never reaches a preserved artifact: capture
//     copies the arm's repo, data dir and transcript, never its config dir.
//
// The token is never logged, never echoed, and never put in an error message.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// credentialMode selects how an arm gets authenticated.
type credentialMode string

const (
	credAuto     credentialMode = "auto"
	credEnv      credentialMode = "env"
	credKeychain credentialMode = "keychain"
	credNone     credentialMode = "none"
)

var credentialModes = []credentialMode{credAuto, credEnv, credKeychain, credNone}

// agentAuthEnv are the variables that authenticate the agent without any file:
// if the operator exported one, passing it through is the cleanest crossing.
var agentAuthEnv = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}

// keychainService is the Keychain item Claude Code stores its login under.
const keychainService = "Claude Code-credentials"

// credentialFile is where Claude Code looks for credentials inside a config dir
// that is not the default ~/.claude.
const credentialFile = ".credentials.json"

// parseCredentialMode validates a --credentials value.
func parseCredentialMode(s string) (credentialMode, error) {
	m := credentialMode(strings.TrimSpace(s))
	for _, known := range credentialModes {
		if m == known {
			return m, nil
		}
	}
	return "", fmt.Errorf("unknown --credentials %q (want %s)", s, joinModes())
}

func joinModes() string {
	ss := make([]string, len(credentialModes))
	for i, m := range credentialModes {
		ss[i] = string(m)
	}
	return strings.Join(ss, ", ")
}

// envCredentials returns the authenticating variables present in the
// environment, as KEY=VALUE, for pass-through to the agent.
func envCredentials() []string {
	var out []string
	for _, k := range agentAuthEnv {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// resolveCredentialMode turns auto into a concrete mode, reporting why. An
// exported key wins over the Keychain: it needs no file on disk.
func resolveCredentialMode(m credentialMode) (credentialMode, string, error) {
	if m != credAuto {
		return m, "", nil
	}
	if names := envCredentialNames(); len(names) > 0 {
		return credEnv, "found " + strings.Join(names, ", ") + " in the environment", nil
	}
	if runtime.GOOS == "darwin" {
		return credKeychain, "no auth env var set; using the macOS Keychain login", nil
	}
	return credNone, "", fmt.Errorf(
		"no agent credentials available: export one of %s, or pass --credentials none for a fake-agent dry run",
		strings.Join(agentAuthEnv, " / "))
}

// envCredentialNames lists which auth variables are set, for logging. It never
// touches their values.
func envCredentialNames() []string {
	var out []string
	for _, k := range agentAuthEnv {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			out = append(out, k)
		}
	}
	return out
}

// credentials provisions arms for one run according to a resolved mode.
type credentials struct {
	mode credentialMode
	// login is the claudeAiOauth-only credential document, read once per suite
	// so a run is not N Keychain prompts. Nil unless mode is keychain.
	login []byte
}

// newCredentials resolves the mode and, for keychain, reads the login once.
func newCredentials(ctx context.Context, m credentialMode) (*credentials, string, error) {
	mode, why, err := resolveCredentialMode(m)
	if err != nil {
		return nil, "", err
	}
	c := &credentials{mode: mode}
	if mode == credKeychain {
		if c.login, err = readKeychainLogin(ctx); err != nil {
			return nil, "", err
		}
	}
	return c, why, nil
}

// env is what to add to the agent's environment (empty except in env mode).
func (c *credentials) env() []string {
	if c.mode != credEnv {
		return nil
	}
	return envCredentials()
}

// provision puts the credential in place for one run and returns the cleanup
// that takes it back out. The cleanup is always safe to call.
func (c *credentials) provision(a *arm) (func(), error) {
	if c.mode != credKeychain || a.configDir == "" {
		return func() {}, nil
	}
	path := filepath.Join(a.configDir, credentialFile)
	if err := os.MkdirAll(a.configDir, 0o700); err != nil {
		return func() {}, fmt.Errorf("prepare arm config dir %s: %w", a.configDir, err)
	}
	if err := os.WriteFile(path, c.login, 0o600); err != nil {
		return func() {}, fmt.Errorf("write arm credentials: %w", err)
	}
	return func() { _ = os.Remove(path) }, nil
}

// readKeychainLogin reads Claude Code's Keychain item and returns a credential
// document carrying ONLY claudeAiOauth.
//
// The stored blob also holds mcpOAuth entries for third-party MCP servers,
// whose tokens and client secrets have nothing to do with this benchmark and
// must not be copied anywhere. Narrowing here, at the read, means no later
// path can leak them by accident.
func readKeychainLogin(ctx context.Context) ([]byte, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("--credentials keychain needs macOS; export one of %s instead",
			strings.Join(agentAuthEnv, " / "))
	}
	out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", keychainService, "-w").Output()
	if err != nil {
		return nil, fmt.Errorf("read the %q Keychain item (is Claude Code logged in?): %w", keychainService, err)
	}
	return narrowLogin(out)
}

// narrowLogin reduces Claude Code's stored credential document to the login
// alone. Split out from the Keychain read so the exclusion rule is testable
// without a logged-in macOS keychain -- it is the security-relevant half.
//
// Its errors never quote the payload: the input is a credential store.
func narrowLogin(stored []byte) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(stored, &doc); err != nil {
		return nil, fmt.Errorf("the %q Keychain item is not JSON", keychainService)
	}
	oauth, ok := doc["claudeAiOauth"]
	if !ok {
		return nil, fmt.Errorf("the %q Keychain item has no claudeAiOauth login", keychainService)
	}
	narrowed, err := json.Marshal(map[string]json.RawMessage{"claudeAiOauth": oauth})
	if err != nil {
		return nil, fmt.Errorf("re-encode the login: %w", err)
	}
	return narrowed, nil
}
