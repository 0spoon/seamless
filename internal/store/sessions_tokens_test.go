package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// SetAmbientSessionTokens OVERWRITES (never accumulates), so a re-harvest of a
// resumed session's grown transcript writes the new absolute total rather than
// double-counting -- this is the resume/double-count rule at the store level.
func TestSetAmbientSessionTokens_OverwritesNotAccumulates(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	s := newSession("01TOK0000000000000000000AA", "cc/tok1")
	s.ExternalSessionID = "tok1-full"
	s.ExternalClient = "claude-code"
	s.Ambient = true
	require.NoError(t, CreateSession(ctx, db, s))

	now := time.Now().UTC()
	first := core.TokenUsage{Input: 100, Cached: 1000, CacheCreation: 200, Output: 50}
	first.Normalize()
	ok, err := SetAmbientSessionTokens(ctx, db, "claude-code", "tok1-full", first, now)
	require.NoError(t, err)
	require.True(t, ok)

	got, _, err := SessionByID(ctx, db, s.ID)
	require.NoError(t, err)
	require.Equal(t, first, got.Tokens)

	// The session resumes; the grown transcript yields a larger cumulative total.
	// The write OVERWRITES -- the stored value is the new total, not first+second.
	second := core.TokenUsage{Input: 180, Cached: 1800, CacheCreation: 360, Output: 90}
	second.Normalize()
	ok, err = SetAmbientSessionTokens(ctx, db, "claude-code", "tok1-full", second, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)

	got, _, err = SessionByID(ctx, db, s.ID)
	require.NoError(t, err)
	require.Equal(t, second, got.Tokens, "overwrite: the latest cumulative total, not the sum of harvests")
}

// The writer is a no-op on an incomplete identity or empty usage, and never
// revives a non-active session.
func TestSetAmbientSessionTokens_GuardsAndNoOps(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	s := newSession("01TOK0000000000000000000BB", "cc/tok2")
	s.ExternalSessionID = "tok2-full"
	s.ExternalClient = "claude-code"
	s.Ambient = true
	require.NoError(t, CreateSession(ctx, db, s))
	now := time.Now().UTC()

	// Empty usage: no-op (a turn with no token record must not blank a prior harvest).
	ok, err := SetAmbientSessionTokens(ctx, db, "claude-code", "tok2-full", core.TokenUsage{}, now)
	require.NoError(t, err)
	require.False(t, ok)

	// Incomplete identity: no-op.
	ok, err = SetAmbientSessionTokens(ctx, db, "", "tok2-full", core.TokenUsage{Input: 1, Total: 1}, now)
	require.NoError(t, err)
	require.False(t, ok)

	// Seed a real total, then complete the session: a later write must not land
	// (the active-only guard freezes a closed session's totals).
	seed := core.TokenUsage{Input: 10, Output: 5}
	seed.Normalize()
	_, err = SetAmbientSessionTokens(ctx, db, "claude-code", "tok2-full", seed, now)
	require.NoError(t, err)

	s.Status = core.SessionCompleted
	s.UpdatedAt = now
	require.NoError(t, UpdateSession(ctx, db, s))

	late := core.TokenUsage{Input: 999, Output: 999}
	late.Normalize()
	ok, err = SetAmbientSessionTokens(ctx, db, "claude-code", "tok2-full", late, now.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, ok, "completed session is frozen")

	got, _, err := SessionByID(ctx, db, s.ID)
	require.NoError(t, err)
	require.Equal(t, seed, got.Tokens, "frozen at what it ended with")
}

// Per-project token totals sum across a project's sessions on the board.
func TestProjectsWithCounts_SumsTokens(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC()

	mk := func(id, name, project string, total int) {
		s := newSession(id, name)
		s.ProjectSlug = project
		s.ExternalSessionID = name + "-full"
		s.ExternalClient = "claude-code"
		s.Ambient = true
		require.NoError(t, CreateSession(ctx, db, s))
		u := core.TokenUsage{Input: total}
		u.Normalize()
		_, err := SetAmbientSessionTokens(ctx, db, "claude-code", name+"-full", u, now)
		require.NoError(t, err)
	}
	mk("01TOKP000000000000000000AA", "cc/p1a", "alpha", 100)
	mk("01TOKP000000000000000000BB", "cc/p1b", "alpha", 250)
	mk("01TOKP000000000000000000CC", "cc/p2a", "beta", 40)

	rows, err := ProjectsWithCounts(ctx, db, ResolveRetrievalWindow("all", now), now, 0)
	require.NoError(t, err)

	byProject := map[string]int{}
	for _, r := range rows {
		byProject[r.Project] = r.TokensTotal
	}
	require.Equal(t, 350, byProject["alpha"], "100 + 250")
	require.Equal(t, 40, byProject["beta"])
}
