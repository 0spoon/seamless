package hooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/store"
)

// codexFixture reads a committed Codex hook-contract fixture. These are the
// ground truth captured live from codex-cli 0.144.5 (see testdata/codex/README);
// the adapter is built and pinned against them, not hand-written JSON.
func codexFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "codex", name))
	require.NoError(t, err)
	return b
}

// The Codex SessionStart payload shares every field this hook reads with Claude
// Code, so the identity decode must surface them and yield the cx/ ambient name.
func TestDecodeSessionStart_CodexFixture(t *testing.T) {
	p := decodeSessionStart(ClientCodex, codexFixture(t, "session-start.input.json"))

	require.Equal(t, "019f7291-40f1-7311-8997-0d497579d27b", p.SessionID)
	require.Equal(t, "/Users/dev/myrepo", p.CWD)
	require.Equal(t, "startup", p.Source)
	require.Contains(t, p.TranscriptPath, "rollout-2026-07-17")
	require.Empty(t, p.AgentType, "a top-level Codex session is not a subagent")

	require.Equal(t, "cx/019f7291", ambientName(ClientCodex, p.SessionID),
		"Codex ambient sessions carry the cx/ prefix off the first 8 id chars")
}

// The whole reason the adapter exists: Codex names the submitted prompt `prompt`,
// Claude Code names it `user_prompt`. decodePrompt must normalize the Codex
// fixture into the internal UserPrompt field so downstream recall is shared code.
func TestDecodePrompt_CodexFixtureNormalizesPromptField(t *testing.T) {
	p := decodePrompt(ClientCodex, codexFixture(t, "user-prompt-submit.input.json"))

	require.Contains(t, p.UserPrompt, "SEAMLESS_SENTINEL_",
		"Codex `prompt` must land in UserPrompt")
	require.Equal(t, "019f7291-40f1-7311-8997-0d497579d27b", p.SessionID)
	require.Equal(t, "/Users/dev/myrepo", p.CWD)
}

// Claude Code bodies already carry `user_prompt`; the Codex normalization must
// never run for them (a stray `prompt` key from a CC client is ignored), so CC
// behavior is provably unchanged.
func TestDecodePrompt_ClaudeCodeIgnoresCodexPromptField(t *testing.T) {
	p := decodePrompt(ClientClaudeCode, []byte(`{"session_id":"abc","cwd":"/w","user_prompt":"cc words"}`))
	require.Equal(t, "cc words", p.UserPrompt)

	// A Codex-shaped body under the CC client does not cross-read `prompt`.
	require.Empty(t, decodePrompt(ClientClaudeCode, []byte(`{"prompt":"cx words"}`)).UserPrompt)
}

// If a body somehow carries both fields, the internal user_prompt wins: the
// Codex fallback only fills an empty UserPrompt, so it can never clobber a value
// that decoded straight into the internal shape.
func TestDecodePrompt_CodexFallbackDoesNotClobberUserPrompt(t *testing.T) {
	p := decodePrompt(ClientCodex, []byte(`{"user_prompt":"already","prompt":"other"}`))
	require.Equal(t, "already", p.UserPrompt)
}

// The discriminator rides on ?client=, not the body. An absent or unknown value
// resolves to Claude Code so any request that predates the flag is unchanged.
func TestClientFromRequest(t *testing.T) {
	mk := func(query string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "/api/hooks/x"+query, nil)
	}
	require.Equal(t, ClientClaudeCode, clientFromRequest(mk("")))
	require.Equal(t, ClientClaudeCode, clientFromRequest(mk("?client=claude-code")))
	require.Equal(t, ClientCodex, clientFromRequest(mk("?client=codex")))
	require.Equal(t, ClientClaudeCode, clientFromRequest(mk("?client=gemini")),
		"an unknown client falls back to Claude Code, never fails the hook")
}

// End to end through the real handler: a Codex SessionStart names a cx/ ambient
// session and injects the briefing, and a Codex UserPromptSubmit whose prompt
// arrives under the `prompt` key still fires recall -- proving the ?client=codex
// query param selects the adapter and everything downstream is shared with CC.
func TestCodexHooks_EndToEnd(t *testing.T) {
	ts, db := newHandlerServer(t)
	ctx := context.Background()
	rec := events.NewRecorder(db)
	codexID := "019f7291-40f1-7311-8997-0d497579d27b"

	_, out := post(t, ts.URL+"/api/hooks/session-start?client=codex", testKey, map[string]any{
		"session_id": codexID, "cwd": "/work/demo", "source": "startup",
	})
	ac := additionalContext(t, out)
	require.Contains(t, ac, "<seam-briefing>")
	require.Contains(t, ac, "Seam session: cx/019f7291 (ambient)",
		"a Codex session gets a cx/ ambient line, not cc/")

	sess, ok, err := store.SessionByName(ctx, db, "cx/019f7291")
	require.NoError(t, err)
	require.True(t, ok, "SessionStart created the cx/ ambient session")
	require.Equal(t, codexID, sess.ExternalSessionID)

	// Codex sends the prompt as `prompt`; a matching one must still return recall.
	_, out = post(t, ts.URL+"/api/hooks/user-prompt-submit?client=codex", testKey, map[string]any{
		"session_id": codexID, "cwd": "/work/demo",
		"prompt": "why does the chroma container fail its health check",
	})
	require.Contains(t, additionalContext(t, out), "chroma-boot-race",
		"the `prompt` field was normalized, so recall fired")

	inj := eventsOfKind(t, rec, core.EventInjected)
	require.Equal(t, "user-prompt-submit", inj[0].Payload["hook"])
	require.Equal(t, sess.ID, inj[0].SessionID,
		"the prompt injection is stamped with the cx/ ambient session")
}
