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
	h := NewHandler(Config{DB: db, Retrieve: ret, Events: events.NewRecorder(db), APIKey: testKey})
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
	ts, db := newHandlerServer(t)
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

	// End to end: the auto-briefing recorded item_ids, so rebuilding the funnel
	// credits each surfaced memory. The full briefing and the subagent briefing
	// both surface the constraint (01A); only the full one surfaces the gotcha
	// (01B).
	ctx := context.Background()
	require.NoError(t, store.RebuildRetrievalStats(ctx, db))
	constraintStat, ok, err := store.GetRetrievalStat(ctx, db, "01A")
	require.NoError(t, err)
	require.True(t, ok, "constraint surfaced by the briefing should have a stats row")
	require.Equal(t, 2, constraintStat.InjectCount)
	gotchaStat, ok, err := store.GetRetrievalStat(ctx, db, "01B")
	require.NoError(t, err)
	require.True(t, ok, "gotcha surfaced by the briefing should have a stats row")
	require.Equal(t, 1, gotchaStat.InjectCount)
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

func eventsOfKind(t *testing.T, rec *events.Recorder, kind core.EventKind) []core.Event {
	t.Helper()
	evs, err := rec.ByKinds(context.Background(), []core.EventKind{kind}, "", "", 200)
	require.NoError(t, err)
	return evs
}

func payloadString(v any) string {
	s, _ := v.(string)
	return s
}

func TestHookEventCapture(t *testing.T) {
	ts, db := newHandlerServer(t)
	ctx := context.Background()
	rec := events.NewRecorder(db)

	// SessionStart creates the ambient session and records an injection stamped
	// with that session's ULID + project.
	_, out := post(t, ts.URL+"/api/hooks/session-start", testKey, map[string]any{
		"session_id": "abcdef12-3456", "cwd": "/work/demo", "source": "startup",
	})
	require.Contains(t, additionalContext(t, out), "<seam-briefing>")

	sess, ok, err := store.SessionByName(ctx, db, "cc/abcdef12")
	require.NoError(t, err)
	require.True(t, ok)

	inj := eventsOfKind(t, rec, core.EventInjected)
	require.Len(t, inj, 1)
	require.Equal(t, sess.ID, inj[0].SessionID, "injection stamped with the ambient session ULID")
	require.Equal(t, "demo", inj[0].ProjectSlug)
	require.Equal(t, "session-start", inj[0].Payload["hook"])
	require.Nil(t, inj[0].Payload["prompt"], "SessionStart carries no user prompt")

	// A matching prompt records an injection carrying the prompt text.
	_, out = post(t, ts.URL+"/api/hooks/user-prompt-submit", testKey, map[string]any{
		"session_id": "abcdef12-3456", "cwd": "/work/demo",
		"user_prompt": "why does the chroma container fail its health check",
	})
	require.Contains(t, additionalContext(t, out), "chroma-boot-race")
	inj = eventsOfKind(t, rec, core.EventInjected)
	require.Len(t, inj, 2) // newest first
	require.Equal(t, "user-prompt-submit", inj[0].Payload["hook"])
	require.Contains(t, payloadString(inj[0].Payload["prompt"]), "chroma container")
	require.Equal(t, sess.ID, inj[0].SessionID)

	// A non-matching prompt records a hook.prompt (recall miss) instead of nothing.
	_, _ = post(t, ts.URL+"/api/hooks/user-prompt-submit", testKey, map[string]any{
		"session_id": "abcdef12-3456", "cwd": "/work/demo", "user_prompt": "weather in paris",
	})
	hp := eventsOfKind(t, rec, core.EventHookPrompt)
	require.Len(t, hp, 1)
	require.Equal(t, false, hp[0].Payload["matched"])
	require.Contains(t, payloadString(hp[0].Payload["prompt"]), "weather in paris")
	require.Equal(t, sess.ID, hp[0].SessionID)
	// Injected count is unchanged by the miss (InjectionsByDay stays accurate).
	require.Len(t, eventsOfKind(t, rec, core.EventInjected), 2)

	// SessionEnd records session.ended with reason + harvested findings.
	transcript := writeTranscript(t,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Did the thing."}]}}`)
	_, _ = post(t, ts.URL+"/api/hooks/session-end", testKey, map[string]any{
		"session_id": "abcdef12-3456", "transcript_path": transcript, "reason": "clear",
	})
	ended := eventsOfKind(t, rec, core.EventSessionEnded)
	require.Len(t, ended, 1)
	require.Equal(t, "clear", ended[0].Payload["reason"])
	require.Contains(t, payloadString(ended[0].Payload["findings"]), "Did the thing.")
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
	resp, endOut := post(t, endURL, testKey, map[string]any{
		"session_id": "abcdef12-3456", "transcript_path": transcript, "reason": "clear",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	// SessionEnd has no hookSpecificOutput variant in Claude Code's schema;
	// including one fails root validation, so the ack must omit it.
	require.NotContains(t, endOut, "hookSpecificOutput")
	require.Equal(t, true, endOut["continue"])

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
	// SessionStart and SessionEnd are command hooks; only UserPromptSubmit stays
	// http, so the bearer key reaches settings via that lone http hook.
	require.Contains(t, string(raw), "seam hook session-start")
	require.Contains(t, string(raw), "seam hook session-end")
	require.Contains(t, string(raw), "8081/api/hooks/user-prompt-submit")
	require.Contains(t, string(raw), "Bearer secret-key")
	// UserPromptSubmit and SessionEnd were added too.
	require.Len(t, hooksObj["UserPromptSubmit"].([]any), 1)
	require.Len(t, hooksObj["SessionEnd"].([]any), 1)
	// The plan-capture hooks are command hooks: ONE PostToolUse entry with the
	// joined matcher, plus SubagentStop and PermissionRequest.
	require.Contains(t, string(raw), "seam hook post-tool-use")
	require.Contains(t, string(raw), "seam hook subagent-stop")
	require.Contains(t, string(raw), "seam hook permission-request")
	ptu := hooksObj["PostToolUse"].([]any)
	require.Len(t, ptu, 1)
	require.Equal(t, "Write|Edit|MultiEdit|ExitPlanMode", ptu[0].(map[string]any)["matcher"])
	require.Len(t, hooksObj["SubagentStop"].([]any), 1)
	pr := hooksObj["PermissionRequest"].([]any)
	require.Len(t, pr, 1)
	require.Equal(t, "ExitPlanMode", pr[0].(map[string]any)["matcher"])

	// Re-install is a no-op.
	res2, err := Install(opts)
	require.NoError(t, err)
	require.False(t, res2.Changed)
	for _, a := range res2.Actions {
		require.Contains(t, a, "unchanged")
	}
}

func TestInstalledStatus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")

	// Missing file -> nothing installed, no error.
	present, err := InstalledStatus(path)
	require.NoError(t, err)
	require.Empty(t, present)

	// After a full install, every event is reported.
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	_, err = Install(InstallOptions{SettingsPath: path, BaseURL: "http://127.0.0.1:8081", APIKey: "k"})
	require.NoError(t, err)

	present, err = InstalledStatus(path)
	require.NoError(t, err)
	require.ElementsMatch(t, InstalledEvents(), present)
	require.Len(t, present, 6)
}

// Mirrors the plan-capture dogfood state: an older installer left UNMARKED
// command hooks (`... seam hook session-start`), which URL-matching alone
// cannot adopt (command hooks have no URL). Install must adopt them by their
// `hook <event>` command instead of appending a duplicate.
func TestInstallAdoptsUnmarkedCommandHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "hooks": {
    "SessionStart": [
      {"matcher": "startup|resume|clear|compact", "hooks": [{"type": "command", "command": "SEAMLESS_CONFIG=/old/seamless.yaml /old/bin/seam hook session-start", "timeout": 10}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": "/old/bin/seam hook session-end", "timeout": 10}]},
      {"seamless_managed": true, "hooks": [{"type": "command", "command": "/old/bin/seam hook session-end", "timeout": 10}]}
    ]
  }
}`), 0o600))

	opts := InstallOptions{SettingsPath: path, BaseURL: "http://127.0.0.1:8081", APIKey: "k", SeamBin: "/new/seam", ConfigPath: "/new/seamless.yaml"}
	res, err := Install(opts)
	require.NoError(t, err)
	require.True(t, res.Changed)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	hooksObj := got["hooks"].(map[string]any)

	// The unmarked command entry is adopted in place, not duplicated.
	ss := hooksObj["SessionStart"].([]any)
	require.Len(t, ss, 1)
	require.Equal(t, true, ss[0].(map[string]any)["seamless_managed"])
	h0 := ss[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	require.Equal(t, "SEAMLESS_CONFIG=/new/seamless.yaml /new/seam hook session-start", h0["command"])

	// Unmarked + marked command entries for one event collapse to one.
	se := hooksObj["SessionEnd"].([]any)
	require.Len(t, se, 1)

	joined := strings.Join(res.Actions, ",")
	require.Contains(t, joined, "SessionStart: adopted")
	require.Contains(t, joined, "SessionEnd: deduped")

	// Re-install is a clean no-op.
	res2, err := Install(opts)
	require.NoError(t, err)
	require.False(t, res2.Changed)
}

// Mirrors the P6 dogfood state: hand-written UNMARKED Seamless hooks (which an
// older installer duplicated) plus a v1 seam_managed entry at :8080. Install
// must adopt/dedupe the seamless-URL entries in place while leaving v1 alone.
func TestInstallAdoptsAndDedupesUnmarkedHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "hooks": {
    "SessionStart": [
      {"matcher": "startup|resume|clear|compact", "hooks": [{"type": "http", "url": "http://127.0.0.1:8081/api/hooks/session-start", "timeout": 10}]},
      {"seamless_managed": true, "matcher": "startup|resume|clear|compact", "hooks": [{"type": "http", "url": "http://127.0.0.1:8081/api/hooks/session-start", "timeout": 10}]},
      {"seam_managed": true, "matcher": "startup|resume|clear|compact", "hooks": [{"type": "http", "url": "http://127.0.0.1:8080/api/hooks/session-start", "timeout": 10}]}
    ],
    "UserPromptSubmit": [
      {"hooks": [{"type": "http", "url": "http://127.0.0.1:8081/api/hooks/user-prompt-submit", "timeout": 5}]}
    ]
  }
}`), 0o600))

	opts := InstallOptions{SettingsPath: path, BaseURL: "http://127.0.0.1:8081", APIKey: "k", SeamBin: "/opt/seam", ConfigPath: "/etc/seamless.yaml"}
	res, err := Install(opts)
	require.NoError(t, err)
	require.True(t, res.Changed)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	hooksObj := got["hooks"].(map[string]any)

	// SessionStart: the two seamless-URL entries collapse to one marked entry;
	// the v1 :8080 seam_managed entry is preserved -> 2 entries total.
	ss := hooksObj["SessionStart"].([]any)
	require.Len(t, ss, 2)
	var marked, v1 int
	for _, e := range ss {
		m := e.(map[string]any)
		if m["seamless_managed"] == true {
			marked++
			// SessionStart is now a command hook running the seam CLI, not http,
			// with the config path baked in so it resolves from any cwd.
			h0 := m["hooks"].([]any)[0].(map[string]any)
			require.Equal(t, "command", h0["type"])
			require.Equal(t, "SEAMLESS_CONFIG=/etc/seamless.yaml /opt/seam hook session-start", h0["command"])
		}
		if m["seam_managed"] == true {
			v1++
			require.Contains(t, m["hooks"].([]any)[0].(map[string]any)["url"], "8080")
		}
	}
	require.Equal(t, 1, marked, "seamless-URL duplicates collapse to one marked entry")
	require.Equal(t, 1, v1, "v1 seam_managed :8080 entry preserved")

	// UserPromptSubmit: the lone unmarked entry is adopted in place (now marked).
	ups := hooksObj["UserPromptSubmit"].([]any)
	require.Len(t, ups, 1)
	require.Equal(t, true, ups[0].(map[string]any)["seamless_managed"])

	// SessionEnd was absent -> added, as a command hook (not http) with the
	// config path baked in so the harvest resolves config from any cwd.
	se := hooksObj["SessionEnd"].([]any)
	require.Len(t, se, 1)
	seHook := se[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	require.Equal(t, "command", seHook["type"])
	require.Equal(t, "SEAMLESS_CONFIG=/etc/seamless.yaml /opt/seam hook session-end", seHook["command"])

	joined := strings.Join(res.Actions, ",")
	require.Contains(t, joined, "SessionStart: deduped")
	require.Contains(t, joined, "UserPromptSubmit: adopted")
	require.Contains(t, joined, "SessionEnd: added")

	// Re-install is now a clean no-op (all entries carry the marker).
	res2, err := Install(opts)
	require.NoError(t, err)
	require.False(t, res2.Changed)
	for _, a := range res2.Actions {
		require.Contains(t, a, "unchanged")
	}
}
