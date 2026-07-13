package hooks

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

const testSID = "abcdef12-3456"

// captureEnv is a hook server wired with the files layer and a plans dir, for
// exercising the plan-capture endpoints end to end.
type captureEnv struct {
	ts       *httptest.Server
	db       *sql.DB
	mgr      *files.Manager
	rec      *events.Recorder
	plansDir string
}

func newCaptureEnv(t *testing.T, pc config.PlanCapture) *captureEnv {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.SetSetting(context.Background(), db,
		store.SettingRepoProjectMap, `{"/work/demo":"demo"}`))

	mgr, err := files.NewManager(filepath.Join(dir, "data"), db, nil)
	require.NoError(t, err)

	plansDir := filepath.Join(dir, "plans")
	require.NoError(t, os.MkdirAll(plansDir, 0o755))

	rec := events.NewRecorder(db)
	ret := retrieve.New(db, nil, config.Budgets{MaxBriefingTokens: 1500, RecallBudgetTokens: 1000}, nil)
	h := NewHandler(Config{
		DB: db, Retrieve: ret, Events: rec, Files: mgr,
		APIKey: testKey, PlanCapture: pc, PlansDir: plansDir,
	})
	mux := http.NewServeMux()
	h.Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &captureEnv{ts: ts, db: db, mgr: mgr, rec: rec, plansDir: plansDir}
}

// startSession creates the ambient session the capture correlates against.
func (e *captureEnv) startSession(t *testing.T) {
	t.Helper()
	resp, _ := post(t, e.ts.URL+"/api/hooks/session-start", testKey, map[string]any{
		"session_id": testSID, "cwd": "/work/demo", "source": "startup",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func (e *captureEnv) writePlanFile(t *testing.T, basename, content string) string {
	t.Helper()
	path := filepath.Join(e.plansDir, basename+".md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func (e *captureEnv) postHook(t *testing.T, endpoint string, body map[string]any) {
	t.Helper()
	resp, _ := post(t, e.ts.URL+"/api/hooks/"+endpoint, testKey, body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func (e *captureEnv) planWrite(t *testing.T, toolName, filePath string) {
	t.Helper()
	e.postHook(t, "post-tool-use", map[string]any{
		"session_id": testSID, "cwd": "/work/demo", "tool_name": toolName,
		"tool_input": map[string]any{"file_path": filePath},
	})
}

// planWriteOut is planWrite returning the decoded hook response, for asserting
// on hookSpecificOutput.
func (e *captureEnv) planWriteOut(t *testing.T, toolName, filePath string) map[string]any {
	t.Helper()
	resp, out := post(t, e.ts.URL+"/api/hooks/post-tool-use", testKey, map[string]any{
		"session_id": testSID, "cwd": "/work/demo", "tool_name": toolName,
		"tool_input": map[string]any{"file_path": filePath},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return out
}

func (e *captureEnv) approve(t *testing.T, toolResponse map[string]any) {
	t.Helper()
	e.postHook(t, "post-tool-use", map[string]any{
		"session_id": testSID, "cwd": "/work/demo", "tool_name": "ExitPlanMode",
		"tool_input": map[string]any{}, "tool_response": toolResponse,
	})
}

func (e *captureEnv) loadNote(t *testing.T, slug string) (core.Note, bool) {
	t.Helper()
	idx, ok, err := store.NoteBySlug(context.Background(), e.db, "demo", slug)
	require.NoError(t, err)
	if !ok {
		return core.Note{}, false
	}
	note, err := e.mgr.Store().ReadNote(idx.FilePath)
	require.NoError(t, err)
	return note, true
}

func (e *captureEnv) sessionPlanMeta(t *testing.T) map[string]any {
	t.Helper()
	sess, ok, err := store.SessionByName(context.Background(), e.db, "cc/abcdef12")
	require.NoError(t, err)
	require.True(t, ok)
	m, _ := sess.Metadata["plan_capture"].(map[string]any)
	return m
}

func TestPlanCaptureIterationUpsert(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: true})
	e.startSession(t)

	path := e.writePlanFile(t, "clever-stallman", "# My Plan\n\nStep one.\n")
	e.planWrite(t, "Write", path)

	note, found := e.loadNote(t, "cc-plan-clever-stallman")
	require.True(t, found)
	require.Equal(t, "My Plan", note.Title)
	require.Equal(t, "demo", note.Project)
	require.Contains(t, note.Tags, "plan:my-plan")
	require.Contains(t, note.Tags, "cc-plan")
	require.Contains(t, note.Tags, "plan-status:draft")
	require.Contains(t, note.Tags, "created-by:agent")
	require.Contains(t, note.Body, "> captured from cc/abcdef12 | clever-stallman.md | iter 1 | git ")
	require.Contains(t, note.Body, "# My Plan\n\nStep one.")
	require.Equal(t, 1, plans.NoteIteration(note))

	meta := e.sessionPlanMeta(t)
	require.Equal(t, "clever-stallman", meta["basename"])
	require.Equal(t, "my-plan", meta["plan_slug"])
	require.Equal(t, "draft", meta["status"])

	evs := eventsOfKind(t, e.rec, core.EventPlanCaptured)
	require.Len(t, evs, 1)
	require.Equal(t, note.ID, evs[0].ItemID)
	require.Equal(t, "clever-stallman", evs[0].Payload["basename"])
	require.Equal(t, "my-plan", evs[0].Payload["plan_slug"])
	require.Contains(t, payloadString(evs[0].Payload["content"]), "Step one.")

	// Second iteration: body and title follow the file; the note identity and
	// the plan composition slug do not.
	e.writePlanFile(t, "clever-stallman", "# My Better Plan\n\nStep two.\n")
	e.planWrite(t, "Edit", path)

	note2, found := e.loadNote(t, "cc-plan-clever-stallman")
	require.True(t, found)
	require.Equal(t, note.ID, note2.ID)
	require.Equal(t, note.Created.Unix(), note2.Created.Unix())
	require.Equal(t, "My Better Plan", note2.Title)
	require.Contains(t, note2.Tags, "plan:my-plan", "title change must not re-slug the plan")
	require.Equal(t, 2, plans.NoteIteration(note2))
	require.Contains(t, note2.Body, "Step two.")
	require.NotContains(t, note2.Body, "Step one.")
	require.Len(t, eventsOfKind(t, e.rec, core.EventPlanCaptured), 2)
}

func TestPlanCaptureIgnoresNonPlanWrites(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: true})
	e.startSession(t)

	e.planWrite(t, "Write", "/work/demo/main.go")
	e.planWrite(t, "Write", filepath.Join(e.plansDir, "nested", "deep.md"))
	e.planWrite(t, "Write", filepath.Join(e.plansDir, "notes.txt"))
	e.planWrite(t, "Write", filepath.Join(e.plansDir, "..", "escape.md"))

	require.Empty(t, eventsOfKind(t, e.rec, core.EventPlanCaptured))
}

func TestPlanCaptureDisabled(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: false, AutoTask: true})
	e.startSession(t)

	path := e.writePlanFile(t, "clever-stallman", "# My Plan\n\nStep one.\n")
	e.planWrite(t, "Write", path)

	_, found := e.loadNote(t, "cc-plan-clever-stallman")
	require.False(t, found)
	require.Empty(t, eventsOfKind(t, e.rec, core.EventPlanCaptured))
}

func TestPlanApprovalFlipsStatusAndCreatesTask(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: true})
	e.startSession(t)
	ctx := context.Background()

	path := e.writePlanFile(t, "clever-stallman", "# My Plan\n\nDo it.\n")
	e.planWrite(t, "Write", path)
	e.approve(t, map[string]any{"filePath": path, "plan": nil})

	note, found := e.loadNote(t, "cc-plan-clever-stallman")
	require.True(t, found)
	require.Contains(t, note.Tags, "plan-status:approved")
	require.Equal(t, 1, plans.NoteIteration(note), "approval must not bump the iteration")

	tasks, err := store.ListTasksForPlan(ctx, e.db, "demo", "", "my-plan")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "Implement plan: My Plan", tasks[0].Title)
	require.Equal(t, core.TaskOpen, tasks[0].Status)
	require.Equal(t, "my-plan", tasks[0].PlanSlug)
	require.Equal(t, "cc/abcdef12", tasks[0].CreatedBy)
	require.Contains(t, tasks[0].Body, "cc-plan-clever-stallman")
	require.Contains(t, tasks[0].Body, note.ID)

	require.Equal(t, "approved", e.sessionPlanMeta(t)["status"])
	require.Len(t, eventsOfKind(t, e.rec, core.EventPlanApproved), 1)

	// Re-approval records another event but never a second task.
	e.approve(t, map[string]any{"filePath": path})
	tasks, err = store.ListTasksForPlan(ctx, e.db, "demo", "", "my-plan")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Len(t, eventsOfKind(t, e.rec, core.EventPlanApproved), 2)
}

func TestPlanApprovalNoAutoTask(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: false})
	e.startSession(t)

	path := e.writePlanFile(t, "clever-stallman", "# My Plan\n\nDo it.\n")
	e.planWrite(t, "Write", path)
	e.approve(t, map[string]any{"filePath": path})

	note, found := e.loadNote(t, "cc-plan-clever-stallman")
	require.True(t, found)
	require.Contains(t, note.Tags, "plan-status:approved")
	tasks, err := store.ListTasksForPlan(context.Background(), e.db, "demo", "", "my-plan")
	require.NoError(t, err)
	require.Empty(t, tasks)
}

func TestPlanApprovalPlanTextFallback(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: true})
	e.startSession(t)

	path := e.writePlanFile(t, "clever-stallman", "# My Plan\n\nV1.\n")
	e.planWrite(t, "Write", path)

	// No usable filePath in the tool_response: the basename comes from the
	// session's plan_capture metadata, the content from tool_response.plan.
	require.NoError(t, os.Remove(path))
	e.approve(t, map[string]any{"filePath": "", "plan": "# My Plan\n\nFinal text.\n"})

	note, found := e.loadNote(t, "cc-plan-clever-stallman")
	require.True(t, found)
	require.Contains(t, note.Tags, "plan-status:approved")
	require.Contains(t, note.Body, "Final text.")
}

func TestPlanApprovalMissingFileFailsOpen(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: true})
	e.startSession(t)

	e.approve(t, map[string]any{"filePath": filepath.Join(e.plansDir, "ghost.md")})

	_, found := e.loadNote(t, "cc-plan-ghost")
	require.False(t, found)
	require.Empty(t, eventsOfKind(t, e.rec, core.EventPlanApproved))
	tasks, err := store.ListTasksForPlan(context.Background(), e.db, "demo", "", "ghost")
	require.NoError(t, err)
	require.Empty(t, tasks)
}

func TestPermissionRequestMarksPresented(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: true})
	e.startSession(t)

	path := e.writePlanFile(t, "clever-stallman", "# My Plan\n\nDo it.\n")
	e.planWrite(t, "Write", path)

	present := map[string]any{
		"session_id": testSID, "cwd": "/work/demo", "tool_name": "ExitPlanMode",
	}
	e.postHook(t, "permission-request", present)

	note, found := e.loadNote(t, "cc-plan-clever-stallman")
	require.True(t, found)
	require.Contains(t, note.Tags, "plan-status:presented")
	require.Contains(t, note.Tags, "plan:my-plan")
	require.Equal(t, "presented", e.sessionPlanMeta(t)["status"])
	require.Len(t, eventsOfKind(t, e.rec, core.EventPlanPresented), 1)

	// Only a draft flips; a second delivery is a no-op.
	e.postHook(t, "permission-request", present)
	require.Len(t, eventsOfKind(t, e.rec, core.EventPlanPresented), 1)

	// A presented plan still approves normally afterwards.
	e.approve(t, map[string]any{"filePath": path})
	note, _ = e.loadNote(t, "cc-plan-clever-stallman")
	require.Contains(t, note.Tags, "plan-status:approved")
}

func TestPermissionRequestWithoutDraftIsNoop(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: true})
	e.startSession(t)

	e.postHook(t, "permission-request", map[string]any{
		"session_id": testSID, "cwd": "/work/demo", "tool_name": "ExitPlanMode",
	})
	require.Empty(t, eventsOfKind(t, e.rec, core.EventPlanPresented))
}

func TestFirstPlanCaptureInjectsRelated(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: true, InjectRelated: true})
	e.startSession(t)
	insertMemory(t, e.db, "01R", "decision", "gardener-pass-order",
		"gardener passes run dedup then staleness then digest", "demo")

	path := e.writePlanFile(t, "clever-stallman", "# Rework the gardener staleness pass\n\nSteps.\n")
	out := e.planWriteOut(t, "Write", path)
	ac := additionalContext(t, out)
	require.Contains(t, ac, "<seam-plan-context>")
	require.Contains(t, ac, "memory_read name=gardener-pass-order")
	require.NotContains(t, ac, "cc-plan-clever-stallman", "the plan's own note is excluded")

	// The injection feeds the funnel like any other.
	evs := eventsOfKind(t, e.rec, core.EventInjected)
	require.Len(t, evs, 1)
	require.Equal(t, "post-tool-use", evs[0].Payload["hook"])

	// Later iterations never re-inject: only the session's first capture does.
	e.writePlanFile(t, "clever-stallman", "# Rework the gardener staleness pass\n\nMore.\n")
	out = e.planWriteOut(t, "Edit", path)
	_, has := out["hookSpecificOutput"]
	require.False(t, has, "no hookSpecificOutput after the first capture")
	require.Len(t, eventsOfKind(t, e.rec, core.EventInjected), 1)
}

func TestFirstPlanCaptureInjectRelatedDisabled(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: true})
	e.startSession(t)
	insertMemory(t, e.db, "01R", "decision", "gardener-pass-order",
		"gardener passes run dedup then staleness then digest", "demo")

	path := e.writePlanFile(t, "clever-stallman", "# Rework the gardener staleness pass\n\nSteps.\n")
	out := e.planWriteOut(t, "Write", path)
	_, has := out["hookSpecificOutput"]
	require.False(t, has)
	require.Empty(t, eventsOfKind(t, e.rec, core.EventInjected))
}

func TestGitHeadStamp(t *testing.T) {
	// A plan capture from a cwd with a readable .git stamps the commit hash.
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	require.NoError(t, os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "HEAD"),
		[]byte("ref: refs/heads/main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "refs", "heads", "main"),
		[]byte("0123456789abcdef0123456789abcdef01234567\n"), 0o644))
	require.Equal(t, "0123456789abcdef0123456789abcdef01234567", gitHead(repo))

	// packed-refs fallback when the loose ref is absent.
	require.NoError(t, os.Remove(filepath.Join(gitDir, "refs", "heads", "main")))
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "packed-refs"),
		[]byte("# pack-refs with: peeled fully-peeled sorted\nfeedfacefeedfacefeedfacefeedfacefeedface refs/heads/main\n"), 0o644))
	require.Equal(t, "feedfacefeedfacefeedfacefeedfacefeedface", gitHead(repo))

	// Detached HEAD is the hash itself.
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "HEAD"),
		[]byte("aaaabbbbccccddddeeeeffff0000111122223333\n"), 0o644))
	require.Equal(t, "aaaabbbbccccddddeeeeffff0000111122223333", gitHead(repo))

	// No repo -> empty, never an error.
	require.Equal(t, "", gitHead(t.TempDir()))
	require.Equal(t, "", gitHead(""))
}
