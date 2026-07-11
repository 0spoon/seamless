package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

const testKey = "hook-test-key"

func newHandlerServer(t *testing.T) (*httptest.Server, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, store.SetSetting(context.Background(), db,
		store.SettingRepoProjectMap, `{"/work/demo":"demo"}`))
	// A constraint and a gotcha in project "demo" for the briefing/prompt matcher.
	insertMemory(t, db, "01A", "constraint", "no-force-push", "never force push to main", "demo")
	insertMemory(t, db, "01B", "gotcha", "chroma-boot-race", "chroma container health check startup race", "demo")
	insertMemory(t, db, "01C", "decision", "ulid-over-uuid", "use ulid identifiers not uuid values", "demo")
	insertMemory(t, db, "01D", "reference", "sqlite-wal", "enable wal journal mode and busy timeout", "demo")

	ret := retrieve.New(db, nil, config.Budgets{MaxBriefingTokens: 1500, RecallBudgetTokens: 1000}, nil)
	h := NewHandler(db, ret, events.NewRecorder(db), testKey, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, db
}

func insertMemory(t *testing.T, db *sql.DB, id, kind, name, desc, project string) {
	t.Helper()
	ctx := context.Background()
	now := core.FormatTime(time.Now().UTC())
	_, err := db.ExecContext(ctx, `
		INSERT INTO memories_index
		    (id, kind, name, description, project, file_path, tags, valid_from,
		     invalid_at, superseded_by, source_session, content_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '[]', ?, NULL, NULL, '', 'h', ?, ?)`,
		id, kind, name, desc, project, "memory/x/"+name+".md", now, now, now)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		INSERT INTO fts (item_id, kind, project, title, name, description, body)
		VALUES (?, 'memory', ?, '', ?, ?, ?)`, id, project, name, desc, desc)
	require.NoError(t, err)
}

func post(t *testing.T, url, key string, body any) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(b)))
	require.NoError(t, err)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	var out map[string]any
	if resp.StatusCode == http.StatusOK {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	}
	return resp, out
}

func additionalContext(t *testing.T, out map[string]any) string {
	t.Helper()
	hso, ok := out["hookSpecificOutput"].(map[string]any)
	require.True(t, ok, "missing hookSpecificOutput: %v", out)
	s, _ := hso["additionalContext"].(string)
	return s
}

func TestSessionStartHook(t *testing.T) {
	ts, _ := newHandlerServer(t)
	url := ts.URL + "/api/hooks/session-start"

	// Bad key -> 401.
	resp, _ := post(t, url, "nope", map[string]any{"cwd": "/work/demo"})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Good key -> briefing in additionalContext.
	resp, out := post(t, url, testKey, map[string]any{"cwd": "/work/demo/sub", "source": "startup"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	ac := additionalContext(t, out)
	require.Contains(t, ac, "<seam-briefing>")
	require.Contains(t, ac, "CONSTRAINT: no-force-push")
	require.Contains(t, ac, "chroma-boot-race")

	// Subagent -> constraints only.
	_, out = post(t, url, testKey, map[string]any{"cwd": "/work/demo", "agent_type": "Explore"})
	ac = additionalContext(t, out)
	require.Contains(t, ac, "CONSTRAINT: no-force-push")
	require.NotContains(t, ac, "chroma-boot-race")
}

func TestUserPromptSubmitHook(t *testing.T) {
	ts, _ := newHandlerServer(t)
	url := ts.URL + "/api/hooks/user-prompt-submit"

	// A matching prompt returns a recall block.
	_, out := post(t, url, testKey, map[string]any{
		"cwd": "/work/demo", "user_prompt": "why does the chroma container fail its health check",
	})
	ac := additionalContext(t, out)
	require.Contains(t, ac, "<seam-recall>")
	require.Contains(t, ac, "chroma-boot-race")

	// A non-matching prompt returns empty additionalContext (never blocks).
	_, out = post(t, url, testKey, map[string]any{"cwd": "/work/demo", "user_prompt": "weather in paris"})
	require.Empty(t, additionalContext(t, out))

	// Empty body is tolerated (200, empty context).
	resp, out := post(t, url, testKey, map[string]any{})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, additionalContext(t, out))
}

func TestAmbientSessionLifecycle(t *testing.T) {
	ts, db := newHandlerServer(t)
	ctx := context.Background()
	startURL := ts.URL + "/api/hooks/session-start"
	endURL := ts.URL + "/api/hooks/session-end"

	// SessionStart creates an ambient session and appends its line to the briefing.
	_, out := post(t, startURL, testKey, map[string]any{
		"session_id": "abcdef12-3456", "cwd": "/work/demo", "source": "startup",
	})
	require.Contains(t, additionalContext(t, out), "Seam session: cc/abcdef12 (ambient)")

	sess, ok, err := store.SessionByName(ctx, db, "cc/abcdef12")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, core.SessionActive, sess.Status)
	require.True(t, sess.Ambient)
	require.Equal(t, "demo", sess.ProjectSlug)
	require.Equal(t, "abcdef12-3456", sess.Metadata["claude_session_id"])

	// A subagent SessionStart gets no ambient session of its own.
	_, out = post(t, startURL, testKey, map[string]any{
		"session_id": "sub00000-0000", "cwd": "/work/demo", "agent_type": "Explore",
	})
	require.NotContains(t, additionalContext(t, out), "(ambient)")
	_, ok, err = store.SessionByName(ctx, db, "cc/sub00000")
	require.NoError(t, err)
	require.False(t, ok, "subagents share the parent session, no ambient row")

	// SessionEnd harvests the transcript's final assistant message and completes.
	transcript := writeTranscript(t,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Shipped the ready-queue."}]}}`)
	resp, _ := post(t, endURL, testKey, map[string]any{
		"session_id": "abcdef12-3456", "transcript_path": transcript, "reason": "clear",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	sess, ok, err = store.SessionByName(ctx, db, "cc/abcdef12")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, core.SessionCompleted, sess.Status)
	require.Equal(t, "(auto-harvested) Shipped the ready-queue.", sess.Findings)

	// Re-delivering SessionEnd is a no-op (findings are not clobbered).
	resp, _ = post(t, endURL, testKey, map[string]any{
		"session_id": "abcdef12-3456", "transcript_path": "", "reason": "other",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	sess, _, _ = store.SessionByName(ctx, db, "cc/abcdef12")
	require.Equal(t, "(auto-harvested) Shipped the ready-queue.", sess.Findings)

	// SessionEnd for an unknown session is a tolerated no-op (still 200).
	resp, _ = post(t, endURL, testKey, map[string]any{"session_id": "unknown0-0000"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestSessionEndRejectsBadKey(t *testing.T) {
	ts, _ := newHandlerServer(t)
	resp, _ := post(t, ts.URL+"/api/hooks/session-end", "nope", map[string]any{"session_id": "x"})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestInstallIdempotentAndPreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	// Pre-existing settings with an unrelated key and a foreign hook.
	require.NoError(t, os.WriteFile(path, []byte(`{
  "model": "opus",
  "hooks": {
    "SessionStart": [
      {"seam_managed": true, "hooks": [{"type": "http", "url": "http://127.0.0.1:8080/api/hooks/session-start"}]}
    ]
  }
}`), 0o600))

	opts := InstallOptions{SettingsPath: path, BaseURL: "http://127.0.0.1:8081", APIKey: "secret-key"}

	res, err := Install(opts)
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.NotEmpty(t, res.BackupPath, "the pre-existing file should be backed up on first change")

	// A backup was written (first change).
	baks, _ := filepath.Glob(path + ".seamless-bak-*")
	require.Len(t, baks, 1)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "opus", got["model"], "unknown top-level key preserved")

	hooksObj := got["hooks"].(map[string]any)
	ss := hooksObj["SessionStart"].([]any)
	// The foreign seam_managed entry survives alongside the new seamless entry.
	require.Len(t, ss, 2)
	require.Contains(t, string(raw), "seamless_managed")
	require.Contains(t, string(raw), "8081/api/hooks/session-start")
	require.Contains(t, string(raw), "Bearer secret-key")
	// UserPromptSubmit and SessionEnd were added too.
	require.Len(t, hooksObj["UserPromptSubmit"].([]any), 1)
	require.Len(t, hooksObj["SessionEnd"].([]any), 1)

	// Re-install is a no-op.
	res2, err := Install(opts)
	require.NoError(t, err)
	require.False(t, res2.Changed)
	for _, a := range res2.Actions {
		require.Contains(t, a, "unchanged")
	}
}
