package hooks

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/store"
)

// writeModelTranscript writes a Claude Code-shaped transcript JSONL and returns its path.
func writeModelTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestTranscriptModel(t *testing.T) {
	t.Run("last main-thread assistant entry wins", func(t *testing.T) {
		path := writeModelTranscript(t,
			`{"type":"user","message":{"content":"hi"}}`,
			`{"type":"assistant","isSidechain":false,"message":{"model":"claude-fable-5"}}`,
			`{"type":"assistant","isSidechain":true,"message":{"model":"claude-opus-4-8"}}`,
			`{"type":"assistant","isSidechain":false,"message":{"model":"claude-fable-5"}}`,
		)
		require.Equal(t, "claude-fable-5", transcriptModel(path))
	})

	t.Run("a model switch is picked up", func(t *testing.T) {
		path := writeModelTranscript(t,
			`{"type":"assistant","isSidechain":false,"message":{"model":"claude-fable-5"}}`,
			`{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-4-8"}}`,
		)
		require.Equal(t, "claude-opus-4-8", transcriptModel(path))
	})

	t.Run("synthetic and sidechain entries never attribute", func(t *testing.T) {
		path := writeModelTranscript(t,
			`{"type":"assistant","isSidechain":false,"message":{"model":"claude-fable-5"}}`,
			`{"type":"assistant","isSidechain":false,"message":{"model":"<synthetic>"}}`,
			`{"type":"assistant","isSidechain":true,"message":{"model":"claude-haiku-4-5"}}`,
		)
		require.Equal(t, "claude-fable-5", transcriptModel(path))
	})

	t.Run("no assistant entries, malformed lines, missing file", func(t *testing.T) {
		require.Empty(t, transcriptModel(writeModelTranscript(t,
			`{"type":"user","message":{"content":"hi"}}`,
			`not json at all`,
		)))
		require.Empty(t, transcriptModel(""))
		require.Empty(t, transcriptModel(filepath.Join(t.TempDir(), "missing.jsonl")))
	})
}

// A Codex hook payload carries the model directly; the ambient session records
// it at session-start and follows a switch reported by a later stop.
func TestAmbientModel_CodexPayload(t *testing.T) {
	ts, db := newHandlerServer(t)
	ctx := context.Background()

	resp, _ := post(t, ts.URL+"/api/hooks/session-start?client=codex", testKey, map[string]any{
		"session_id": "0199f7a2-codex-session", "cwd": "/work/demo",
		"source": "startup", "model": "gpt-5.5",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	sess, ok, err := store.SessionByName(ctx, db, "cx/0199f7a2")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "gpt-5.5", sess.Model)

	resp, _ = post(t, ts.URL+"/api/hooks/stop?client=codex", testKey, map[string]any{
		"session_id": "0199f7a2-codex-session", "cwd": "/work/demo", "model": "gpt-5.5-codex",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	sess, _, err = store.SessionByName(ctx, db, "cx/0199f7a2")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.5-codex", sess.Model)
}

// Claude Code payloads carry no model; the ambient session's model is sniffed
// from the transcript on session-start and kept current on every prompt.
func TestAmbientModel_ClaudeTranscriptSniff(t *testing.T) {
	ts, db := newHandlerServer(t)
	ctx := context.Background()

	// Startup with no transcript yet: the session exists with no attribution.
	resp, _ := post(t, ts.URL+"/api/hooks/session-start", testKey, map[string]any{
		"session_id": "abc12345-cc-session", "cwd": "/work/demo", "source": "startup",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	sess, ok, err := store.SessionByName(ctx, db, "cc/abc12345")
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, sess.Model)

	// First prompt after an assistant turn: the transcript names the model.
	path := writeModelTranscript(t,
		`{"type":"assistant","isSidechain":false,"message":{"model":"claude-fable-5"}}`,
	)
	resp, _ = post(t, ts.URL+"/api/hooks/user-prompt-submit", testKey, map[string]any{
		"session_id": "abc12345-cc-session", "cwd": "/work/demo",
		"user_prompt": "keep going", "transcript_path": path,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	sess, _, err = store.SessionByName(ctx, db, "cc/abc12345")
	require.NoError(t, err)
	require.Equal(t, "claude-fable-5", sess.Model)

	// A /model switch shows up on the next prompt's sniff.
	switched := writeModelTranscript(t,
		`{"type":"assistant","isSidechain":false,"message":{"model":"claude-fable-5"}}`,
		`{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-4-8"}}`,
	)
	resp, _ = post(t, ts.URL+"/api/hooks/user-prompt-submit", testKey, map[string]any{
		"session_id": "abc12345-cc-session", "cwd": "/work/demo",
		"user_prompt": "and again", "transcript_path": switched,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	sess, _, err = store.SessionByName(ctx, db, "cc/abc12345")
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-8", sess.Model)

	// A resume sniffs at session-start too.
	require.NoError(t, store.SetSessionModelByName(ctx, db, "cc/abc12345", "stale-value"))
	resp, _ = post(t, ts.URL+"/api/hooks/session-start", testKey, map[string]any{
		"session_id": "abc12345-cc-session", "cwd": "/work/demo",
		"source": "resume", "transcript_path": switched,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	sess, _, err = store.SessionByName(ctx, db, "cc/abc12345")
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-8", sess.Model)
}
