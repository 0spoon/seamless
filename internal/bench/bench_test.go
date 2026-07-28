package bench

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/demokit"
	"github.com/0spoon/seamless/internal/store"
)

func TestScenarios_TableShape(t *testing.T) {
	scs := Scenarios()
	require.NotEmpty(t, scs)
	seen := map[string]bool{}
	for _, sc := range scs {
		require.NotEmpty(t, sc.Name, "scenario with empty name")
		require.NotEmpty(t, sc.Prompt, "scenario %s: empty prompt", sc.Name)
		require.NotNil(t, sc.Seed, "scenario %s: nil seed", sc.Name)
		require.False(t, seen[sc.Name], "duplicate scenario name %s", sc.Name)
		seen[sc.Name] = true
	}
}

func TestScenarioByName(t *testing.T) {
	sc, ok := ScenarioByName("auth-refresh")
	require.True(t, ok)
	require.Equal(t, "auth-refresh", sc.Name)
	require.False(t, sc.RequiresRecall, "auth-refresh is briefing-surfaced and must stay headless-runnable")

	_, ok = ScenarioByName("no-such-scenario")
	require.False(t, ok)
}

// TestSeedAuthRefresh_State asserts the seeded fixture against DB and file
// state -- expected memories by name/kind, plan topology, backdated finding
// age -- never against briefing text: the briefing layout churns by design
// and is itself the benchmark's regression surface.
func TestSeedAuthRefresh_State(t *testing.T) {
	dataDir := t.TempDir()
	repo := t.TempDir()

	s, err := demokit.New(dataDir)
	require.NoError(t, err)
	sc, ok := ScenarioByName("auth-refresh")
	require.True(t, ok)
	require.NoError(t, sc.Seed(s, repo))
	require.NoError(t, s.Close())

	ctx := context.Background()
	db, err := store.Open(filepath.Join(dataDir, "seam.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	// Memories: active, correctly named, kinded, and scoped to myapp.
	wantKinds := map[string]core.MemoryKind{
		"refresh-token-single-use":     core.KindConstraint,
		"auth-cookies-httponly-secure": core.KindConstraint,
		"auth-cookies-samesite-lax":    core.KindConstraint,
		"edge-cache-gotcha":            core.KindGotcha,
		"rate-limit-not-in-memory":     core.KindGotcha,
		"persist-refresh-tokens":       core.KindGotcha,
		"chroma-boot-race":             core.KindGotcha,
		"deploy-runbook":               core.KindRunbook,
		"postgres-timeouts":            core.KindGotcha,
	}
	mems, err := store.ActiveMemories(ctx, db, "myapp")
	require.NoError(t, err)
	require.Len(t, mems, len(wantKinds))
	now := time.Now().UTC()
	for _, m := range mems {
		kind, ok := wantKinds[m.Name]
		require.True(t, ok, "unexpected memory %s", m.Name)
		require.Equal(t, kind, m.Kind, "memory %s", m.Name)
		require.Equal(t, "myapp", m.Project, "memory %s", m.Name)
		require.True(t, m.Created.Before(now.Add(-24*time.Hour)), "memory %s not backdated: %s", m.Name, m.Created)
	}
	// Files are the source of truth: each memory exists on disk too.
	for name := range wantKinds {
		_, err := os.Stat(filepath.Join(dataDir, "memory", "myapp", name+".md"))
		require.NoError(t, err, "memory file %s missing", name)
	}

	// Plan topology: step 5 is the sole ready step, step 6 is blocked on it,
	// four steps are done.
	ready, err := store.ReadyTasksForPlan(ctx, db, "myapp", "auth-refresh")
	require.NoError(t, err)
	require.Len(t, ready, 1)
	require.Equal(t, "Rate-limit POST /auth/refresh (per-IP and per-family)", ready[0].Title)

	blocked, err := store.BlockedTasksForPlan(ctx, db, "myapp", "auth-refresh")
	require.NoError(t, err)
	require.Len(t, blocked, 1)
	require.Equal(t, "Emit metrics and an alert for refresh-reuse revocations", blocked[0].Task.Title)
	require.Len(t, blocked[0].Blockers, 1)
	require.Equal(t, ready[0].ID, blocked[0].Blockers[0].ID)

	done, err := store.ListTasksForPlan(ctx, db, "myapp", core.TaskDone, "auth-refresh")
	require.NoError(t, err)
	require.Len(t, done, 4)

	// The backdated finding: a completed session ~18h old carrying the
	// continue-work summary, visible to the briefing's data source.
	sess, ok, err := store.SessionByName(ctx, db, "cc/1a2b3c4d")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, core.SessionCompleted, sess.Status)
	require.Equal(t, authRefreshFinding, sess.Findings)
	age := now.Sub(sess.UpdatedAt)
	require.InDelta(t, (18 * time.Hour).Minutes(), age.Minutes(), 30, "finding age drifted from ~18h: %s", age)

	recents, err := store.RecentFindings(ctx, db, "myapp", 5)
	require.NoError(t, err)
	var found bool
	for _, r := range recents {
		found = found || r.ID == sess.ID
	}
	require.True(t, found, "the 18h finding is not in RecentFindings")

	// The failed pool-timeout trial, tied to that session.
	trials, err := store.QueryTrials(ctx, db, store.TrialFilter{Lab: "refresh-500s"})
	require.NoError(t, err)
	require.Len(t, trials, 1)
	require.Equal(t, core.OutcomeFail, trials[0].Outcome)
	require.Equal(t, sess.ID, trials[0].SessionID)
	require.Equal(t, "myapp", trials[0].ProjectSlug)

	// The repo mapping binds sessions starting in the demo repo to myapp.
	repoMap, err := store.RepoProjectMap(ctx, db)
	require.NoError(t, err)
	require.Equal(t, "myapp", repoMap[repo])
}

// TestSeedAuthRefresh_NoRepoMapping covers the "" repo path: seeding must
// succeed and simply skip the mapping.
func TestSeedAuthRefresh_NoRepoMapping(t *testing.T) {
	dataDir := t.TempDir()
	s, err := demokit.New(dataDir)
	require.NoError(t, err)
	sc, ok := ScenarioByName("auth-refresh")
	require.True(t, ok)
	require.NoError(t, sc.Seed(s, ""))
	require.NoError(t, s.Close())

	ctx := context.Background()
	db, err := store.Open(filepath.Join(dataDir, "seam.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	repoMap, err := store.RepoProjectMap(ctx, db)
	require.NoError(t, err)
	require.Empty(t, repoMap)
}
