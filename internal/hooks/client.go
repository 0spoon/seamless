package hooks

// Client identifies the agent CLI a hook request came from. It selects the
// ambient session-name prefix (cc/ vs cx/) so a Claude Code agent and a Codex
// agent working the same machine get distinct, self-describing session names.
// The client rides on the hook payload; the --client plumbing that populates it
// from the request is a separate concern -- everything here only needs a Client
// value, and normalizeClient turns an absent or unknown one into ClientClaudeCode
// so any path not yet taught the discriminator keeps its Claude Code behavior.
type Client string

const (
	// ClientClaudeCode is the default: Claude Code sends no discriminator, so an
	// empty client resolves here and existing cc/ behavior is unchanged.
	ClientClaudeCode Client = "claude-code"
	// ClientCodex is the Codex CLI, whose ambient sessions are named cx/.
	ClientCodex Client = "codex"
)

// ambientPrefix is the session-name prefix for the client: cc/ for Claude Code,
// cx/ for Codex. An unrecognized client falls back to Claude Code's prefix -- a
// hook must never fail to name a session over an unknown discriminator.
func (c Client) ambientPrefix() string {
	if c == ClientCodex {
		return "cx/"
	}
	return "cc/"
}

// normalizeClient maps a raw client string (from the hook payload / --client
// flag) to a known Client, defaulting an empty or unrecognized value to
// ClientClaudeCode so existing Claude Code hooks -- which send no discriminator
// -- are untouched.
func normalizeClient(raw string) Client {
	if Client(raw) == ClientCodex {
		return ClientCodex
	}
	return ClientClaudeCode
}
