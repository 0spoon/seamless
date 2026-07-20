package hooks

import (
	"fmt"
	"slices"
	"strings"
)

// Client identifies the agent host a hook request came from. It selects the
// ambient session-name prefix (cc/ vs cx/) so a Claude Code agent and a Codex
// agent working the same machine get distinct, self-describing session names.
type Client string

const (
	// ClientClaudeCode is the default: Claude Code sends no discriminator, so an
	// empty client resolves here and existing cc/ behavior is unchanged.
	ClientClaudeCode Client = "claude-code"
	// ClientCodex is the shared local Codex app/CLI/IDE host, whose ambient
	// sessions are named cx/.
	ClientCodex Client = "codex"
)

// HookClients lists every accepted hook client discriminator. It is the
// canonical set behind request parsing, programmatic profile selection, error
// text, and the seam CLI's test-pinned copy.
var HookClients = []Client{ClientClaudeCode, ClientCodex}

// ambientPrefix is the session-name prefix for the client: cc/ for Claude Code,
// cx/ for Codex. Callers receive Clients only after boundary validation; the
// default arm preserves the zero-value Claude behavior for internal construction.
func (c Client) ambientPrefix() string {
	if c == ClientCodex {
		return "cx/"
	}
	return "cc/"
}

// externalIdentity names the canonical client discriminator persisted beside a
// full external session id. Boundary parsers reject invalid present values before
// store identity construction; the zero value remains the internal Claude default.
func (c Client) externalIdentity() string {
	if c == ClientCodex {
		return string(ClientCodex)
	}
	return string(ClientClaudeCode)
}

// parseClient distinguishes an absent discriminator from a present value.
// Absence is the backward-compatible Claude Code default; every present value
// must belong to HookClients, including a present empty string.
func parseClient(raw string, present bool) (Client, error) {
	if !present {
		return ClientClaudeCode, nil
	}
	client := Client(raw)
	if slices.Contains(HookClients, client) {
		return client, nil
	}
	return "", fmt.Errorf("invalid hook client %q: valid values are %s", raw, hookClientNames())
}

func hookClientNames() string {
	values := make([]string, len(HookClients))
	for i, client := range HookClients {
		values[i] = string(client)
	}
	return strings.Join(values, ", ")
}
