package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
)

// pairableConfig is a server-role install advertising url, with key as its
// bearer key -- the one state client-config is allowed to print for.
func pairableConfig(url, key string) config.Config {
	return config.Config{
		Role:          config.RoleServer,
		AdvertisedURL: url,
		MCP:           config.MCP{APIKey: key},
	}
}

// Every state whose pairing command would look right and fail on the other
// machine. The assertion is on the message, not just the error: a refusal whose
// text does not name the fix leaves the operator exactly where they started.
func TestClientConfigRefusals(t *testing.T) {
	tests := []struct {
		name  string
		cfg   config.Config
		wants []string
	}{
		{
			name: "client role has no pairing to hand out",
			cfg: config.Config{
				Role:          config.RoleClient,
				AdvertisedURL: "https://box.example:8081",
				MCP:           config.MCP{APIKey: "topsecret"},
			},
			wants: []string{"role: client", "https://box.example:8081", "THAT machine"},
		},
		{
			name:  "empty key (whitespace is empty)",
			cfg:   pairableConfig("https://box.example:8081", "   "),
			wants: []string{"mcp.api_key is empty", "openssl rand -hex 32"},
		},
		{
			name: "loopback derived from a loopback bind",
			cfg: config.Config{
				Addr: "127.0.0.1:8081",
				MCP:  config.MCP{APIKey: "topsecret"},
			},
			wants: []string{
				"http://127.0.0.1:8081 is a loopback address",
				"server_url",
				"addr: 0.0.0.0:8081",
				"restart the daemon",
				"A SECOND USER ON THE SAME BOX",
				"seamlessd install-hooks --server-url http://127.0.0.1:8081 --api-key <key>",
			},
		},
		{
			name:  "loopback advertised by name",
			cfg:   pairableConfig("http://localhost:8081", "topsecret"),
			wants: []string{"is a loopback address", "addr: 0.0.0.0:8081"},
		},
		{
			name:  "loopback advertised as IPv6",
			cfg:   pairableConfig("http://[::1]:8081", "topsecret"),
			wants: []string{"is a loopback address", "addr: 0.0.0.0:8081"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := clientConfigReport(&buf, tt.cfg, false)
			require.Error(t, err)
			for _, want := range tt.wants {
				require.Contains(t, err.Error(), want)
			}
			require.Empty(t, buf.String(), "a refusal must not print a pairing command")
		})
	}
}

// A non-loopback host that is not a loopback NAME must still pair: the refusal
// is about reachability, not about looking unusual.
func TestClientConfigAcceptsReachableHosts(t *testing.T) {
	for _, url := range []string{"http://192.168.5.43:8081", "https://box.example:8081", "http://[2001:db8::1]:8081"} {
		var buf bytes.Buffer
		require.NoError(t, clientConfigReport(&buf, pairableConfig(url, "topsecret"), false), url)
		require.Contains(t, buf.String(), url)
	}
}

func TestClientConfigWarnsOnPlainHTTP(t *testing.T) {
	var plain bytes.Buffer
	require.NoError(t, clientConfigReport(&plain, pairableConfig("http://box.example:8081", "topsecret"), false))
	out := plain.String()
	require.Contains(t, out, "warning:")
	require.Contains(t, out, "plain http")
	// A warning, never a refusal: the commands are still printed.
	require.Contains(t, out, "SEAMLESS_SERVER_URL=http://box.example:8081")

	var secure bytes.Buffer
	require.NoError(t, clientConfigReport(&secure, pairableConfig("https://box.example:8081", "topsecret"), false))
	require.NotContains(t, secure.String(), "warning:")
}

// The three canonical forms, byte for byte. They are a contract shared with
// docs/install, docs/install.ps1 and install-hooks: an env var or flag renamed
// on one side only leaves an operator pasting a command that installs nothing.
func TestClientConfigPrintsTheCanonicalCommands(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, clientConfigReport(&buf, pairableConfig("https://box.example:8081", "topsecret"), false))
	out := buf.String()

	require.Contains(t, out,
		"curl -fsSL https://thereisnospoon.org/install | SEAMLESS_SERVER_URL=https://box.example:8081 SEAMLESS_MCP_API_KEY=topsecret sh")
	require.Contains(t, out,
		"$env:SEAMLESS_SERVER_URL='https://box.example:8081'; $env:SEAMLESS_MCP_API_KEY='topsecret'; irm https://thereisnospoon.org/install.ps1 | iex")
	require.Contains(t, out,
		"seamlessd install-hooks --server-url https://box.example:8081 --api-key topsecret")
	require.Contains(t, out, minClientVersion)
}

func TestClientConfigRedactsTheKey(t *testing.T) {
	const key = "topsecret"
	cfg := pairableConfig("https://box.example:8081", key)

	var plain, redacted bytes.Buffer
	require.NoError(t, clientConfigReport(&plain, cfg, false))
	require.NoError(t, clientConfigReport(&redacted, cfg, true))
	out := redacted.String()

	require.NotContains(t, out, key)
	require.Contains(t, out, "https://box.example:8081", "only the key is masked, not the URL")
	require.Contains(t, out, "SEAMLESS_MCP_API_KEY="+redactedKey+" sh")
	require.Contains(t, out, "$env:SEAMLESS_MCP_API_KEY='"+redactedKey+"'")
	require.Contains(t, out, "--api-key "+redactedKey)

	// Same command shape, proven rather than eyeballed: the redacted output is
	// the plain one with the key substituted, plus the trailing note that says
	// so. A reader of a pasted ticket sees exactly the command they will run.
	require.True(t, strings.HasPrefix(out, strings.ReplaceAll(plain.String(), key, redactedKey)),
		"redacted output should differ from plain only by the key and the trailing note")
	require.Contains(t, out, "--redact:")

	// The placeholder has to survive being pasted into a shell line, so it may
	// not carry metacharacters that would turn into a redirect or a glob.
	require.NotContains(t, redactedKey, "<")
	require.NotContains(t, redactedKey, ">")
	require.NotContains(t, redactedKey, "$")
}
