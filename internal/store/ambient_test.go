package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

func TestActiveAmbientProjects(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC()

	mk := func(name, project string, updatedAgo time.Duration, ambient bool, status core.SessionStatus) {
		id, err := core.NewID()
		require.NoError(t, err)
		require.NoError(t, CreateSession(ctx, db, core.Session{
			ID: id, Name: name, ProjectSlug: project, Status: status, Ambient: ambient,
			CreatedAt: now.Add(-updatedAgo), UpdatedAt: now.Add(-updatedAgo),
		}))
	}

	// Two projects with recent ambient activity ("other" touched most recently),
	// plus rows that must be excluded from the fallback set.
	mk("cc/demo1", "demo", 30*time.Minute, true, core.SessionActive)
	mk("cc/demo2", "demo", 20*time.Minute, true, core.SessionActive)   // same project, dedup
	mk("cc/other1", "other", 5*time.Minute, true, core.SessionActive)  // most recent overall
	mk("cc/stale", "stale", 8*time.Hour, true, core.SessionActive)     // outside the window
	mk("cc/done", "done", 1*time.Minute, true, core.SessionCompleted)  // not active
	mk("sess/x", "explicit", 1*time.Minute, false, core.SessionActive) // not ambient

	projects, err := ActiveAmbientProjects(ctx, db, AmbientScope{}, ambientWindowForTest)
	require.NoError(t, err)
	// Distinct projects only, ordered by most recent activity: other before demo.
	require.Equal(t, []string{"other", "demo"}, projects)

	// The project-scoped lookup returns that project's latest ambient session.
	sess, ok, err := LatestActiveAmbientSessionForProject(ctx, db, AmbientScope{}, "demo", ambientWindowForTest)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "cc/demo2", sess.Name, "returns the most recently updated ambient in the project")

	// A project with no ambient session yields found=false, not another project's.
	_, ok, err = LatestActiveAmbientSessionForProject(ctx, db, AmbientScope{}, "nope", ambientWindowForTest)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestActiveAmbientSessionsForProject(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC()

	mk := func(name, project string, updatedAgo time.Duration, ambient bool, status core.SessionStatus) {
		id, err := core.NewID()
		require.NoError(t, err)
		require.NoError(t, CreateSession(ctx, db, core.Session{
			ID: id, Name: name, ProjectSlug: project, Status: status, Ambient: ambient,
			CreatedAt: now.Add(-updatedAgo), UpdatedAt: now.Add(-updatedAgo),
		}))
	}

	// Two active ambients in "demo" (the same-repo concurrency case), plus rows
	// that must be excluded: an older-window ambient, a completed one, a
	// non-ambient one, and an ambient in a different project.
	mk("cc/demoA", "demo", 10*time.Minute, true, core.SessionActive)
	mk("cc/demoB", "demo", 3*time.Minute, true, core.SessionActive) // most recent in demo
	mk("cc/stale", "demo", 8*time.Hour, true, core.SessionActive)   // outside window
	mk("cc/done", "demo", 1*time.Minute, true, core.SessionCompleted)
	mk("sess/x", "demo", 1*time.Minute, false, core.SessionActive)
	mk("cc/other", "other", 1*time.Minute, true, core.SessionActive)

	sessions, err := ActiveAmbientSessionsForProject(ctx, db, AmbientScope{}, "demo", ambientWindowForTest)
	require.NoError(t, err)
	require.Len(t, sessions, 2, "two concurrent same-project ambients -- the ambiguity resolveSession must refuse")
	require.Equal(t, "cc/demoB", sessions[0].Name, "most recent first")
	require.Equal(t, "cc/demoA", sessions[1].Name)

	// A lone ambient in a project is unambiguous (the solo ergonomic).
	solo, err := ActiveAmbientSessionsForProject(ctx, db, AmbientScope{}, "other", ambientWindowForTest)
	require.NoError(t, err)
	require.Len(t, solo, 1)
}

// ambientWindowForTest mirrors the MCP server's ambientFallbackWindow so the
// store test exercises the same recency boundary.
const ambientWindowForTest = 6 * time.Hour

// TestActiveAmbientByAgentProcess pins the join an MCP connection makes to its
// own agent's ambient session: same host, same process, active, ambient -- and
// nothing else. The stamp follows the session to whatever process runs it now.
func TestActiveAmbientByAgentProcess(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC()

	mk := func(name, host, proc string, ambient bool, status core.SessionStatus) string {
		id, err := core.NewID()
		require.NoError(t, err)
		require.NoError(t, CreateSession(ctx, db, core.Session{
			ID: id, Name: name, ProjectSlug: "demo", Status: status, Ambient: ambient, Host: host,
			CreatedAt: now, UpdatedAt: now,
		}))
		require.NoError(t, SetSessionAgentProcess(ctx, db, id, proc))
		return id
	}

	mine := mk("cc/mine", "alpha", "100.1", true, core.SessionActive)
	mk("cc/sibling", "alpha", "200.2", true, core.SessionActive)     // another agent on this machine
	mk("cc/remote", "beta", "100.1", true, core.SessionActive)       // same identity, other machine
	mk("cc/ended", "alpha", "100.1", true, core.SessionCompleted)    // this process's previous session
	mk("sess/explicit", "alpha", "100.1", false, core.SessionActive) // never stamped by a hook in practice

	got, err := ActiveAmbientByAgentProcess(ctx, db, "alpha", "100.1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, mine, got[0].ID)

	got, err = ActiveAmbientByAgentProcess(ctx, db, "alpha", "")
	require.NoError(t, err)
	require.Empty(t, got, "an empty identity names no process")

	// A resume in a new process moves the stamp; the old identity stops matching.
	require.NoError(t, SetSessionAgentProcess(ctx, db, mine, "300.3"))
	got, err = ActiveAmbientByAgentProcess(ctx, db, "alpha", "100.1")
	require.NoError(t, err)
	require.Empty(t, got)
	got, err = ActiveAmbientByAgentProcess(ctx, db, "alpha", "300.3")
	require.NoError(t, err)
	require.Len(t, got, 1)

	require.Error(t, SetSessionAgentProcess(ctx, db, "01NOSUCHSESSION0000000000", "1.1"),
		"stamping a missing session is an error, not a silent no-op")
}

// TestReviveExpiredAgentSession pins what proof of life may reopen: a session the
// idle reaper expired, owned by the live process that is calling, and nothing
// else -- not a completed session, not one of two candidates, not a live one.
func TestReviveExpiredAgentSession(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC()
	mk := func(name, proc string, status core.SessionStatus) string {
		id, err := core.NewID()
		require.NoError(t, err)
		require.NoError(t, CreateSession(ctx, db, core.Session{
			ID: id, Name: name, ProjectSlug: "demo", Status: status, Ambient: true, Host: "alpha",
			CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
		}))
		require.NoError(t, SetSessionAgentProcess(ctx, db, id, proc))
		return id
	}

	reaped := mk("cc/reaped", "100.1", core.SessionExpired)
	got, ok, err := ReviveExpiredAgentSession(ctx, db, "alpha", "100.1", now)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, reaped, got.ID)
	require.Equal(t, core.SessionActive, got.Status)
	require.WithinDuration(t, now, got.UpdatedAt, time.Second, "revival is activity: the reaper must not take it straight back")

	_, ok, err = ReviveExpiredAgentSession(ctx, db, "alpha", "100.1", now)
	require.NoError(t, err)
	require.False(t, ok, "a live session is not revived again")

	mk("cc/ended", "200.2", core.SessionCompleted)
	_, ok, err = ReviveExpiredAgentSession(ctx, db, "alpha", "200.2", now)
	require.NoError(t, err)
	require.False(t, ok, "a session ended on purpose stays ended")

	mk("cc/one", "300.3", core.SessionExpired)
	mk("cc/two", "300.3", core.SessionExpired)
	_, ok, err = ReviveExpiredAgentSession(ctx, db, "alpha", "300.3", now)
	require.NoError(t, err)
	require.False(t, ok, "two candidates for one process: nothing to choose between")

	_, ok, err = ReviveExpiredAgentSession(ctx, db, "beta", "100.1", now)
	require.NoError(t, err)
	require.False(t, ok, "another machine's process is another process")
}

// TestAmbientScope_CallerExcludesOtherAgents pins the fallback's one new rule: a
// caller that named its process is never inferred into a session stamped with a
// DIFFERENT process. Unstamped sessions (older clients) stay candidates.
func TestAmbientScope_CallerExcludesOtherAgents(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC()
	mk := func(name, project, proc string) {
		id, err := core.NewID()
		require.NoError(t, err)
		require.NoError(t, CreateSession(ctx, db, core.Session{
			ID: id, Name: name, ProjectSlug: project, Status: core.SessionActive, Ambient: true,
			CreatedAt: now, UpdatedAt: now,
		}))
		require.NoError(t, SetSessionAgentProcess(ctx, db, id, proc))
	}
	mk("cc/peer", "other", "200.2")
	mk("cc/legacy", "legacy", "")

	projects, err := ActiveAmbientProjects(ctx, db, AmbientScope{}, ambientWindowForTest)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"other", "legacy"}, projects, "an unidentified caller sees every ambient")

	projects, err = ActiveAmbientProjects(ctx, db, AmbientScope{Caller: "100.1"}, ambientWindowForTest)
	require.NoError(t, err)
	require.Equal(t, []string{"legacy"}, projects, "another agent's session is not a candidate")

	_, ok, err := LatestActiveAmbientSessionForProject(ctx, db, AmbientScope{Caller: "100.1"}, "other", ambientWindowForTest)
	require.NoError(t, err)
	require.False(t, ok)
	sessions, err := ActiveAmbientSessionsForProject(ctx, db, AmbientScope{Caller: "200.2"}, "other", ambientWindowForTest)
	require.NoError(t, err)
	require.Len(t, sessions, 1, "a caller's own stamp is still a candidate")
}
