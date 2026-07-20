package console

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

func TestHarnessOf(t *testing.T) {
	cases := []struct {
		name string
		sess core.Session
		want string
	}{
		{"stored client wins", core.Session{ExternalClient: "codex", Name: "cc/ab12cd34"}, "codex"},
		{"cc prefix", core.Session{Name: "cc/ab12cd34"}, "claude-code"},
		{"cx prefix", core.Session{Name: "cx/019f7bc6"}, "codex"},
		{"explicit session unknown", core.Session{Name: "sess/1gr88ege"}, ""},
		{"zero session", core.Session{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, harnessOf(tc.sess))
		})
	}
}

func TestModelShort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"claude-fable-5", "fable-5"},
		{"claude-opus-4-8", "opus-4-8"},
		{"claude-haiku-4-5-20251001", "haiku-4-5"},
		{"gpt-5.5", "gpt-5.5"},
		{"", ""},
		// A degenerate id that shortens to nothing stays verbatim.
		{"claude-", "claude-"},
		// Pins the Go fallback for a bare build-date id. interactions.js
		// deliberately diverges here (it drops the model half); neither input
		// is reachable today, so parity is pinned per side, not unified.
		{"-20251001", "-20251001"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			require.Equal(t, tc.want, modelShort(tc.in))
		})
	}
}

func TestAgentPill(t *testing.T) {
	t.Run("harness and model", func(t *testing.T) {
		got := string(agentPill("claude-code", "claude-fable-5"))
		require.Contains(t, got, `class="agent-pill cc"`)
		require.Contains(t, got, "cc · fable-5")
		require.Contains(t, got, `title="Claude Code · claude-fable-5"`)
	})
	t.Run("codex tone", func(t *testing.T) {
		got := string(agentPill("codex", "gpt-5.5"))
		require.Contains(t, got, `class="agent-pill cx"`)
		require.Contains(t, got, "cx · gpt-5.5")
	})
	t.Run("model only stays neutral", func(t *testing.T) {
		require.Equal(t,
			`<span class="agent-pill" title="claude-fable-5">fable-5</span>`,
			string(agentPill("", "claude-fable-5")))
	})
	t.Run("unknown client passes through neutral", func(t *testing.T) {
		got := string(agentPill("gemini-cli", "gemini-3"))
		require.Contains(t, got, `class="agent-pill"`)
		require.Contains(t, got, "gemini-cli · gemini-3")
	})
	t.Run("both empty renders nothing", func(t *testing.T) {
		require.Empty(t, string(agentPill("", "")))
	})
	t.Run("escapes html in stored strings", func(t *testing.T) {
		// Exact output pins BOTH contexts: the double-quoted title attribute
		// (quotes must be escaped, or the payload breaks out of the attribute)
		// and the element text.
		require.Equal(t,
			`<span class="agent-pill" title="&lt;script&gt;&#34;x&#34;&lt;/script&gt;">&lt;script&gt;&#34;x&#34;&lt;/script&gt;</span>`,
			string(agentPill("", `<script>"x"</script>`)))
	})
}
