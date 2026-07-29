package bench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/demokit"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/llm"
	"github.com/0spoon/seamless/internal/store"
)

// ---------------------------------------------------------------------------
// repo fixtures: the demo repo as scripts/fixture/make-myapp.sh leaves it,
// plus the right-answer and wrong-answer shapes an agent can leave behind.
// ---------------------------------------------------------------------------

const baselineGoMod = `module myapp

go 1.21
`

const baselineServerGo = `package main

import (
	"html/template"
	"net/http"
)

type server struct {
	tokens *tokenStore
	home   *template.Template
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHome)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/auth/refresh", s.handleRefresh)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	return mux
}

func (s *server) handleHome(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
`

// baselineAuthGo is the untouched handler. Note the TODO: it says "no rate
// limit" in a COMMENT, which is precisely what the code-token scan must not
// mistake for a limiter.
const baselineAuthGo = `package main

import (
	"encoding/json"
	"net/http"
)

// handleRefresh rotates the caller's refresh token.
//
// TODO: this endpoint has no rate limit. A credential-stuffing attack can
// hammer it. Add a limiter before the token lookup.
func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c, err := r.Cookie("refresh")
	if err != nil || c.Value == "" {
		http.Error(w, "missing refresh token", http.StatusUnauthorized)
		return
	}
	pair, err := s.tokens.rotate(c.Value)
	if err != nil {
		http.Error(w, "invalid refresh token", http.StatusUnauthorized)
		return
	}
	setAuthCookie(w, "session", pair.access)
	setAuthCookie(w, "refresh", pair.refresh)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
`

// baselineTokensGo is the pre-existing in-memory token store. It is a map under
// a mutex and has nothing to do with rate limiting: an assertion that reads it
// as "the limiter is in-memory" is over-fitting on the wrong file.
const baselineTokensGo = `package main

import (
	"errors"
	"sync"
)

type tokenStore struct {
	mu       sync.Mutex
	familyOf map[string]string
	live     map[string]string
}

func newTokenStore() *tokenStore {
	return &tokenStore{familyOf: map[string]string{}, live: map[string]string{}}
}

var errReuse = errors.New("refresh token reuse detected; family revoked")
`

// wiredAuthGo is the handler after either kind of limiter lands: the endpoint
// goes through it before the token lookup.
const wiredAuthGo = `package main

import (
	"encoding/json"
	"net/http"
)

func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.limiter.Allow(r.Context(), clientIP(r), familyOf(r)) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	c, err := r.Cookie("refresh")
	if err != nil || c.Value == "" {
		http.Error(w, "missing refresh token", http.StatusUnauthorized)
		return
	}
	pair, err := s.tokens.rotate(c.Value)
	if err != nil {
		http.Error(w, "invalid refresh token", http.StatusUnauthorized)
		return
	}
	setAuthCookie(w, "session", pair.access)
	setAuthCookie(w, "refresh", pair.refresh)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
`

// sharedLimiterGo is the right answer: the counter lives in Redis, shared by
// every instance behind the load balancer.
const sharedLimiterGo = `package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type refreshLimiter struct {
	rdb    *redis.Client
	limit  int64
	window time.Duration
}

func newRefreshLimiter(rdb *redis.Client, limit int64, window time.Duration) *refreshLimiter {
	return &refreshLimiter{rdb: rdb, limit: limit, window: window}
}

func (l *refreshLimiter) Allow(ctx context.Context, ip, family string) bool {
	key := fmt.Sprintf("ratelimit:refresh:%s:%s", ip, family)
	n, err := l.rdb.Incr(ctx, key).Result()
	if err != nil {
		return true
	}
	if n == 1 {
		l.rdb.Expire(ctx, key, l.window)
	}
	return n <= l.limit
}
`

// inMemoryLimiterGo is the wrong answer the memory rate-limit-not-in-memory
// exists to prevent: a per-process map that resets on every instance.
const inMemoryLimiterGo = `package main

import (
	"net"
	"sync"
	"time"
)

type refreshLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
}

func newRefreshLimiter(limit int, window time.Duration) *refreshLimiter {
	return &refreshLimiter{hits: map[string][]time.Time{}, limit: limit, window: window}
}

func (l *refreshLimiter) Allow(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-l.window)
	kept := l.hits[host][:0]
	for _, t := range l.hits[host] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.hits[host] = append(kept, time.Now())
	return len(l.hits[host]) <= l.limit
}
`

func baselineRepo() map[string]string {
	return map[string]string{
		"go.mod":    baselineGoMod,
		"server.go": baselineServerGo,
		"auth.go":   baselineAuthGo,
		"tokens.go": baselineTokensGo,
	}
}

func sharedStorageRepo() map[string]string {
	r := baselineRepo()
	r["go.mod"] = baselineGoMod + "\nrequire github.com/redis/go-redis/v9 v9.5.1\n"
	r["auth.go"] = wiredAuthGo
	r["ratelimit.go"] = sharedLimiterGo
	return r
}

func inMemoryRepo() map[string]string {
	r := baselineRepo()
	r["auth.go"] = wiredAuthGo
	r["ratelimit.go"] = inMemoryLimiterGo
	return r
}

// ---------------------------------------------------------------------------
// run-directory fixtures: the frozen artifact layout, synthesized.
// ---------------------------------------------------------------------------

// runShape says which of a run's Seamless behaviours to synthesize into the
// preserved data dir. All false means a Seamless-ful arm whose agent ignored
// the mechanism entirely.
type runShape struct {
	briefing     bool
	briefingHook string // override the recorded hook marker (default session-start)
	readMemory   bool
	moveTask     bool
	finding      bool
	globalWrite  bool // a memory_write that misfired out of the project
	noise        bool // recall + a failed tool call + a mishap, for metrics
}

func fullRun() runShape {
	return runShape{briefing: true, readMemory: true, moveTask: true, finding: true, noise: true}
}

// scenarioFixture is the seeded state a synthesized run dir belongs to: which
// scenario to seed, and the stable markers (project, load-bearing memory, plan
// step) the run's events refer to. One per scenario, so both graders are
// exercised through the same synthesis path.
type scenarioFixture struct {
	scenario   string
	prompt     string
	project    string
	memory     string
	plan       string
	step       string
	written    string // the memory name the run's memory_write records
	findings   string // the summary its session_end carries
	transcript string // the one transcript line the judge layer sees
}

var authRefreshFixture = scenarioFixture{
	scenario:   authRefreshName,
	prompt:     "continue where we left off",
	project:    authRefreshProject,
	memory:     authRefreshMemory,
	plan:       authRefreshPlan,
	step:       authRefreshStep5,
	written:    "refresh-limiter-keys",
	findings:   "Shipped step 5: the refresh limiter keys on IP + token family in Redis.",
	transcript: "picking up step 5",
}

// newRunDir writes an auth-refresh run directory.
func newRunDir(t *testing.T, cond Condition, files map[string]string, diff string, shape *runShape) RunArtifacts {
	t.Helper()
	return newScenarioRunDir(t, authRefreshFixture, cond, files, diff, shape)
}

// newScenarioRunDir writes a run directory in the layout artifacts.go freezes
// and returns it loaded through LoadRun, exactly as cmd/seambench report would.
// A nil shape synthesizes a vanilla arm: no data dir is preserved.
func newScenarioRunDir(t *testing.T, fx scenarioFixture, cond Condition, files map[string]string, diff string, shape *runShape) RunArtifacts {
	t.Helper()
	dir := t.TempDir()

	repo := filepath.Join(dir, RepoDirName)
	require.NoError(t, os.MkdirAll(repo, 0o755))
	for name, body := range files {
		path := filepath.Join(repo, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	if diff != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, DiffFile), []byte(diff), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, TranscriptFile),
		[]byte(`{"type":"assistant","text":"`+fx.transcript+`"}`+"\n"), 0o644))

	if shape != nil {
		seedRunDataDir(t, dir, fx, *shape)
	}
	require.NoError(t, WriteRunRecord(dir, RunRecord{
		Scenario: fx.scenario, Condition: cond, Run: 1, Version: "test",
		Prompt: fx.prompt, Metrics: Metrics{Turns: 9, InputTokens: 1000},
	}))

	_, a, err := LoadRun(dir)
	require.NoError(t, err)
	return a
}

// seedRunDataDir builds the arm's data dir the way the runner would (scenario
// seed into a fresh demokit dir) and then records the events the run itself
// would have produced. The seed writes no events, so everything in the log
// below is unambiguously the run's.
func seedRunDataDir(t *testing.T, dir string, fx scenarioFixture, shape runShape) {
	t.Helper()
	dataDir := filepath.Join(dir, DataDirName)
	s, err := demokit.New(dataDir)
	require.NoError(t, err)
	sc, ok := ScenarioByName(fx.scenario)
	require.True(t, ok)
	require.NoError(t, sc.Seed(s, ""))
	require.NoError(t, s.Close())

	ctx := context.Background()
	db, err := store.Open(filepath.Join(dataDir, "seam.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	rec := events.NewRecorder(db)
	sessionID, err := core.NewID()
	require.NoError(t, err)
	at := time.Now().UTC()
	put := func(kind core.EventKind, project, itemID string, payload map[string]any) {
		at = at.Add(time.Second)
		_, err := rec.Record(ctx, core.Event{
			TS: at, Kind: kind, SessionID: sessionID, ProjectSlug: project,
			ItemID: itemID, Payload: payload,
		})
		require.NoError(t, err)
	}
	tool := func(name string, args map[string]any) {
		put(core.EventToolCall, fx.project, "",
			map[string]any{"tool": name, "args": args, "duration_ms": 12})
	}

	mem, ok, err := store.MemoryByName(ctx, db, fx.project, fx.memory)
	require.NoError(t, err)
	require.True(t, ok)

	if shape.briefing {
		hook := shape.briefingHook
		if hook == "" {
			hook = "session-start"
		}
		put(core.EventInjected, fx.project, "", map[string]any{
			"hook": hook, "claude_session_id": "b2e7c9d4", "content": "<seam-briefing>...</seam-briefing>",
			"item_ids": []string{mem.ID}, "item_ids_exact": true,
		})
	}
	if shape.readMemory {
		tool("memory_read", map[string]any{"name": fx.memory})
		put(core.EventMemoryRead, fx.project, mem.ID, map[string]any{"name": fx.memory})
	}
	if shape.moveTask {
		tasks, err := store.ListTasksForPlan(ctx, db, fx.project, "", fx.plan)
		require.NoError(t, err)
		var target core.Task
		for _, tk := range tasks {
			if tk.Title == fx.step {
				target = tk
			}
		}
		require.NotEmpty(t, target.ID, "seeded plan %s has no step %q", fx.plan, fx.step)
		inProgress := core.TaskInProgress
		_, err = store.UpdateTask(ctx, db, target.ID, store.TaskPatch{Status: &inProgress}, sessionID, at)
		require.NoError(t, err)
		tool("tasks_claim", map[string]any{"id": target.ID})
		put(core.EventTaskTransition, fx.project, target.ID,
			map[string]any{"action": "claim", "status": string(core.TaskInProgress)})
	}
	if shape.finding {
		tool("memory_write", map[string]any{"name": fx.written})
		put(core.EventMemoryWritten, fx.project, mem.ID, map[string]any{"name": fx.written})
		tool("session_end", map[string]any{"findings": "done"})
		put(core.EventSessionEnded, fx.project, "", map[string]any{
			"claims_released": 1, "findings": fx.findings,
		})
	}
	if shape.globalWrite {
		tool("memory_write", map[string]any{"name": "stray-note", "project": "global"})
		put(core.EventMemoryWritten, "", "01JSTRAY", map[string]any{"name": "stray-note"})
	}
	if shape.noise {
		tool("recall", map[string]any{"query": "cache headers"})
		put(core.EventInjected, fx.project, "", map[string]any{
			"query": "cache headers", "item_ids": []string{mem.ID}, "source": "recall",
		})
		tool("recall", map[string]any{"query": "a helper module that does not exist"})
		put(core.EventRecallMiss, fx.project, "", map[string]any{
			"query": "a helper module that does not exist", "scope": "all", "limit": 10, "source": "recall",
		})
		put(core.EventToolCall, fx.project, "", map[string]any{
			"tool": "tasks_update", "is_error": true, "error": "task already claimed",
		})
		put(core.EventAgentMishap, fx.project, "", map[string]any{
			"description": "edited the wrong file first",
		})
	}
}

func detailsFor(t *testing.T, res Result, substr string) string {
	t.Helper()
	for _, d := range res.Details {
		if strings.Contains(d, substr) {
			return d
		}
	}
	require.Failf(t, "missing detail", "no Details line contains %q\n%s", substr, strings.Join(res.Details, "\n"))
	return ""
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestAuthRefreshGrader_PassesOnSharedStorageLimiter(t *testing.T) {
	shape := fullRun()
	a := newRunDir(t, DefaultConditions()[1], sharedStorageRepo(), "--- a/auth.go\n+++ b/auth.go\n+limiter\n", &shape)

	res, err := authRefreshGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.True(t, res.Pass, strings.Join(res.Details, "\n"))

	require.Contains(t, detailsFor(t, res, "rate limiter on the refresh path"), "PASS")
	require.Contains(t, detailsFor(t, res, "limiter counter is not per-process"), "redis")
	require.Contains(t, detailsFor(t, res, "SessionStart briefing injected"), "PASS")
	require.Contains(t, detailsFor(t, res, "load-bearing memory consulted"), "PASS")
	require.Contains(t, detailsFor(t, res, "plan step moved"), "PASS")
	require.Contains(t, detailsFor(t, res, "durable finding at session_end"), "PASS")
	require.Contains(t, detailsFor(t, res, "durable writes scoped to myapp"), "PASS")
	require.Contains(t, detailsFor(t, res, "judge:"), "no LLM judge configured")

	m := res.Metrics
	require.Equal(t, 1, m.Injections)
	require.Equal(t, 1, m.MemoryReads)
	require.Equal(t, 1, m.MemoryWrites)
	require.Equal(t, 2, m.Recalls)
	require.Equal(t, 1, m.RecallMisses)
	require.Equal(t, 1, m.TaskTransitions)
	require.Equal(t, 1, m.SessionFindings)
	require.Equal(t, 1, m.Mishaps)
	require.Equal(t, 1, m.ToolErrors)
	require.Equal(t, 7, m.ToolCalls)
	require.Equal(t, 1, m.ToolCallsByName["memory_read"])
	require.Equal(t, 2, m.ToolCallsByName["recall"])
	// Run-shape fields belong to the runner; the grader never writes them.
	require.Zero(t, m.Turns)
	require.Zero(t, m.InputTokens)
	require.Zero(t, m.CostUSD)
	require.Zero(t, m.DurationMS)
}

func TestAuthRefreshGrader_FailsOnInMemoryLimiter(t *testing.T) {
	shape := fullRun()
	a := newRunDir(t, DefaultConditions()[1], inMemoryRepo(), "--- a/auth.go\n+++ b/auth.go\n+limiter\n", &shape)

	res, err := authRefreshGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass, strings.Join(res.Details, "\n"))

	// The limiter IS wired to the endpoint -- it is the storage that is wrong,
	// and the failing line has to say so.
	require.Contains(t, detailsFor(t, res, "rate limiter on the refresh path"), "PASS")
	line := detailsFor(t, res, "limiter counter is not per-process")
	require.Contains(t, line, "FAIL")
	require.Contains(t, line, "per-process map")
	require.Contains(t, line, "ratelimit.go")
	// The pre-existing in-memory token store must not be what triggered it.
	require.NotContains(t, line, "tokens.go")
}

func TestAuthRefreshGrader_FailsWhenNothingChanged(t *testing.T) {
	a := newRunDir(t, DefaultConditions()[0], baselineRepo(), "", nil)

	res, err := authRefreshGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass)
	require.Contains(t, detailsFor(t, res, "the working tree changed"), "changed nothing")
	require.Contains(t, detailsFor(t, res, "rate limiter on the refresh path"), "no rate-limiting code")
	require.Contains(t, detailsFor(t, res, "limiter counter is not per-process"), "FAIL")
}

// A vanilla arm preserves no data dir. That is the control, not a broken run:
// the event layer reports n/a and the verdict rests on the repo assertions.
func TestAuthRefreshGrader_VanillaArmGradesOnRepoAlone(t *testing.T) {
	a := newRunDir(t, DefaultConditions()[0], sharedStorageRepo(), "--- a/auth.go\n+++ b/auth.go\n+limiter\n", nil)
	require.Empty(t, a.DataDir)

	res, err := authRefreshGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.True(t, res.Pass, strings.Join(res.Details, "\n"))
	require.Contains(t, detailsFor(t, res, "event: n/a"), "the control")
	require.Zero(t, res.Metrics.ToolCalls)
	require.Nil(t, res.Metrics.ToolCallsByName)
}

// The observed event checks measure how the agent used Seamless; only defects
// gate. A run that solved the task without touching a memory still passes, or
// the uplift number would punish the arm for the agent's habits.
func TestAuthRefreshGrader_ObservedEventChecksDoNotGate(t *testing.T) {
	shape := runShape{briefing: true}
	a := newRunDir(t, DefaultConditions()[1], sharedStorageRepo(), "--- a/auth.go\n+++ b/auth.go\n+limiter\n", &shape)

	res, err := authRefreshGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.True(t, res.Pass, strings.Join(res.Details, "\n"))
	require.Contains(t, detailsFor(t, res, "load-bearing memory consulted"), "FAIL")
	require.Contains(t, detailsFor(t, res, "plan step moved"), "still open")
	require.Contains(t, detailsFor(t, res, "durable finding at session_end"), "no session was ended")
}

func TestAuthRefreshGrader_MechanismDefectsGate(t *testing.T) {
	t.Run("no injection reached the agent", func(t *testing.T) {
		shape := runShape{readMemory: true, moveTask: true, finding: true}
		a := newRunDir(t, DefaultConditions()[1], sharedStorageRepo(), "d\n", &shape)
		res, err := authRefreshGrader.Grade(context.Background(), a)
		require.NoError(t, err)
		require.False(t, res.Pass)
		require.Contains(t, detailsFor(t, res, "SessionStart briefing injected"), "no hook injection")
	})

	t.Run("a durable write misfired to global", func(t *testing.T) {
		shape := fullRun()
		shape.globalWrite = true
		a := newRunDir(t, DefaultConditions()[1], sharedStorageRepo(), "d\n", &shape)
		res, err := authRefreshGrader.Grade(context.Background(), a)
		require.NoError(t, err)
		require.False(t, res.Pass)
		line := detailsFor(t, res, "durable writes scoped to myapp")
		require.Contains(t, line, "FAIL")
		require.Contains(t, line, "global")
	})

	// The marker is the hook's identity, not its spelling: the event-name form
	// grades the same as the CLI-arg form.
	t.Run("hook marker spelling is normalized", func(t *testing.T) {
		shape := fullRun()
		shape.briefingHook = "SessionStart"
		a := newRunDir(t, DefaultConditions()[1], sharedStorageRepo(), "d\n", &shape)
		res, err := authRefreshGrader.Grade(context.Background(), a)
		require.NoError(t, err)
		require.True(t, res.Pass, strings.Join(res.Details, "\n"))
	})
}

// GradeRunDir is the whole grading entry point: a run directory in, a verdict
// out, with the runner's run-shape metrics left on the manifest where they
// were written.
func TestGradeRunDir(t *testing.T) {
	shape := fullRun()
	a := newRunDir(t, DefaultConditions()[1], sharedStorageRepo(), "d\n", &shape)

	rec, res, err := GradeRunDir(context.Background(), a.Dir, nil)
	require.NoError(t, err)
	require.True(t, res.Pass, strings.Join(res.Details, "\n"))
	require.Equal(t, authRefreshName, rec.Scenario)
	require.Equal(t, 9, rec.Metrics.Turns, "the runner's run-shape metrics survive grading")
	require.Zero(t, res.Metrics.Turns, "the grader never writes run-shape fields")
	require.Equal(t, 1, res.Metrics.Injections)

	j := &fakeJudge{verdict: JudgeVerdict{Pass: true, Reason: "cited the constraint"}}
	_, res, err = GradeRunDir(context.Background(), a.Dir, j)
	require.NoError(t, err)
	require.Equal(t, 1, j.calls)
	require.Contains(t, detailsFor(t, res, "judge/advisory"), "PASS")

	t.Run("unknown scenario", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, WriteRunRecord(dir, RunRecord{Scenario: "no-such-scenario", Run: 1}))
		_, _, err := GradeRunDir(context.Background(), dir, nil)
		require.ErrorContains(t, err, "unknown scenario")
	})
}

func TestGrade_UngradeableArtifacts(t *testing.T) {
	t.Run("no preserved repo dir", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, WriteRunRecord(dir, RunRecord{Scenario: authRefreshName, Run: 1}))
		_, a, err := LoadRun(dir)
		require.NoError(t, err)
		_, err = authRefreshGrader.Grade(context.Background(), a)
		require.ErrorIs(t, err, ErrMissingArtifacts)
	})

	// A data dir with no database would let store.Open create an empty one and
	// every event check would then fail for the wrong reason.
	t.Run("data dir without a database", func(t *testing.T) {
		a := newRunDir(t, DefaultConditions()[1], sharedStorageRepo(), "d\n", nil)
		require.NoError(t, os.MkdirAll(filepath.Join(a.Dir, DataDirName), 0o755))
		_, a, err := LoadRun(a.Dir)
		require.NoError(t, err)
		_, err = authRefreshGrader.Grade(context.Background(), a)
		require.ErrorIs(t, err, ErrMissingArtifacts)
	})
}

// ---------------------------------------------------------------------------
// judge layer
// ---------------------------------------------------------------------------

type fakeJudge struct {
	verdict JudgeVerdict
	err     error
	calls   int
	last    JudgeRequest
}

func (f *fakeJudge) Judge(_ context.Context, req JudgeRequest) (JudgeVerdict, error) {
	f.calls++
	f.last = req
	return f.verdict, f.err
}

func TestGrade_JudgeIsAdditiveAndDegrades(t *testing.T) {
	shape := fullRun()
	a := newRunDir(t, DefaultConditions()[1], sharedStorageRepo(), "d\n", &shape)

	tests := []struct {
		name    string
		judge   *fakeJudge
		want    string
		calls   int
		wantRun bool // the run's own verdict, which the judge must never change
	}{
		{
			name:    "provider outage degrades",
			judge:   &fakeJudge{err: fmt.Errorf("llm.OpenAI.Complete: %w", llm.ErrUnavailable)},
			want:    "judge: degraded (provider unavailable)",
			calls:   1,
			wantRun: true,
		},
		{
			name:    "local misconfiguration degrades loudly",
			judge:   &fakeJudge{err: fmt.Errorf("llm.OpenAI.Complete: %w", llm.ErrConfig)},
			want:    "judge: degraded (local misconfiguration",
			calls:   1,
			wantRun: true,
		},
		{
			name:    "a failing verdict is advisory only",
			judge:   &fakeJudge{verdict: JudgeVerdict{Pass: false, Reason: "never explained the multi-instance problem"}},
			want:    "judge/advisory: FAIL: never explained",
			calls:   1,
			wantRun: true,
		},
		{
			name:    "a passing verdict is recorded",
			judge:   &fakeJudge{verdict: JudgeVerdict{Pass: true, Reason: "cited the shared-storage constraint"}},
			want:    "judge/advisory: PASS",
			calls:   1,
			wantRun: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := WithJudge(authRefreshGrader, tt.judge)
			res, err := g.Grade(context.Background(), a)
			require.NoError(t, err)
			require.Equal(t, tt.wantRun, res.Pass, strings.Join(res.Details, "\n"))
			require.Equal(t, tt.calls, tt.judge.calls)
			detailsFor(t, res, tt.want)
			require.Equal(t, authRefreshName, tt.judge.last.Scenario)
			require.Equal(t, "continue where we left off", tt.judge.last.Prompt)
			require.Contains(t, tt.judge.last.Rubric, "shared across")
			require.Contains(t, tt.judge.last.Transcript, "picking up step 5")
		})
	}
}

func TestWithJudge_DoesNotMutateTheScenarioGrader(t *testing.T) {
	j := &fakeJudge{verdict: JudgeVerdict{Pass: true}}
	g := WithJudge(authRefreshGrader, j)
	require.NotSame(t, Grader(authRefreshGrader), g)
	require.Nil(t, authRefreshGrader.judge, "the package-level grader must stay judge-less")

	// A nil judge leaves the grader untouched rather than half-enabling a layer.
	require.Same(t, Grader(authRefreshGrader), WithJudge(authRefreshGrader, nil))
}

func TestGrade_JudgeSkippedWithoutATranscript(t *testing.T) {
	shape := fullRun()
	a := newRunDir(t, DefaultConditions()[1], sharedStorageRepo(), "d\n", &shape)
	require.NoError(t, os.Remove(filepath.Join(a.Dir, TranscriptFile)))
	_, a, err := LoadRun(a.Dir)
	require.NoError(t, err)

	j := &fakeJudge{}
	res, err := WithJudge(authRefreshGrader, j).Grade(context.Background(), a)
	require.NoError(t, err)
	require.Zero(t, j.calls)
	require.Contains(t, detailsFor(t, res, "judge:"), "preserved no transcript")
}

func TestParseJudgeVerdict(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    JudgeVerdict
		wantErr bool
	}{
		{name: "bare object", out: `{"pass":true,"reason":"ok"}`, want: JudgeVerdict{Pass: true, Reason: "ok"}},
		{
			name: "fenced with prose",
			out:  "Here is my verdict:\n```json\n{\"pass\": false, \"reason\": \"wrong step\"}\n```\n",
			want: JudgeVerdict{Pass: false, Reason: "wrong step"},
		},
		{name: "no object", out: "the run looks fine to me", wantErr: true},
		{name: "malformed object", out: `{"pass": yes}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseJudgeVerdict(tt.out)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNewJudge_NilChatIsNoJudge(t *testing.T) {
	require.Nil(t, NewJudge(nil))
}

// ---------------------------------------------------------------------------
// scanning
// ---------------------------------------------------------------------------

// A comment is a wish, not a design: "should live in shared storage" over an
// in-memory map must not read as shared storage.
func TestRepoScan_CommentsAreNotEvidence(t *testing.T) {
	dir := t.TempDir()
	src := `package main

import "sync"

// TODO: this counter should live in shared storage (redis) so it survives a
// restart and is seen by every instance.
type refreshLimiter struct {
	mu   sync.Mutex
	hits map[string]int
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ratelimit.go"), []byte(src), 0o644))

	tree, err := loadRepoTree(dir, "d\n")
	require.NoError(t, err)
	require.Len(t, tree.Files, 1)
	require.True(t, tree.Files[0].Parsed)
	require.True(t, tree.Files[0].Maps)
	require.NotContains(t, tree.Files[0].Code, "redis")

	ok, detail := limiterUsesSharedStorage().fn(tree)
	require.False(t, ok)
	require.Contains(t, detail, "per-process map")
}

// An unparseable file still grades: it falls back to raw text rather than
// disappearing from the tree.
func TestRepoScan_UnparseableFileFallsBackToRawText(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.go"), []byte("package main\nfunc ( {"), 0o644))

	tree, err := loadRepoTree(dir, "")
	require.NoError(t, err)
	require.Len(t, tree.Files, 1)
	require.False(t, tree.Files[0].Parsed)
	require.Contains(t, tree.Files[0].Code, "package main")
}

func TestRepoScan_SkipsGitAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "hook.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "real.go"), []byte("package main\n"), 0o644))
	if err := os.Symlink(filepath.Join(dir, "real.go"), filepath.Join(dir, "link.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	tree, err := loadRepoTree(dir, "")
	require.NoError(t, err)
	require.Len(t, tree.Files, 1)
	require.Equal(t, "real.go", tree.Files[0].Path)
}

func TestLoadRepoTree_MissingRoot(t *testing.T) {
	_, err := loadRepoTree("", "")
	require.ErrorIs(t, err, ErrMissingArtifacts)

	_, err = loadRepoTree(filepath.Join(t.TempDir(), "absent"), "")
	require.ErrorIs(t, err, ErrMissingArtifacts)
}

func TestScenario_AuthRefreshHasAGrader(t *testing.T) {
	sc, ok := ScenarioByName(authRefreshName)
	require.True(t, ok)
	require.NotNil(t, sc.Grader)
}

func TestNormalizeHook(t *testing.T) {
	require.Equal(t, normalizeHook("session-start"), normalizeHook("SessionStart"))
	require.NotEqual(t, normalizeHook("session-start"), normalizeHook("subagent-start"))
}

func TestErrorsAreWrappedNotSwallowed(t *testing.T) {
	// A grader error must stay identifiable through the wrap chain.
	err := fmt.Errorf("outer: %w", fmt.Errorf("%w: inner", ErrMissingArtifacts))
	require.True(t, errors.Is(err, ErrMissingArtifacts))
}
