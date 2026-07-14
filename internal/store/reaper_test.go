package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

func TestExpireStaleSessions(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC()
	cutoff := now.Add(-core.SessionIdleTTL)

	mk := func(name string, updatedAt time.Time, status core.SessionStatus) string {
		id, err := core.NewID()
		require.NoError(t, err)
		require.NoError(t, CreateSession(ctx, db, core.Session{
			ID: id, Name: name, ProjectSlug: "seamless", Status: status,
			CreatedAt: updatedAt, UpdatedAt: updatedAt,
		}))
		return id
	}

	live := mk("cc/live", now.Add(-5*time.Minute), core.SessionActive)         // fresh -> kept
	stale := mk("sess/stale", now.Add(-2*time.Hour), core.SessionActive)       // idle -> reaped
	done := mk("cc/done", now.Add(-3*time.Hour), core.SessionCompleted)        // terminal -> untouched
	borderline := mk("sess/edge", now.Add(-time.Minute), core.SessionActive)   // just active -> kept

	reaped, err := ExpireStaleSessions(ctx, db, cutoff)
	require.NoError(t, err)
	require.Len(t, reaped, 1, "only the idle active session is reaped")
	require.Equal(t, stale, reaped[0].ID)

	// The reaped session flips to expired but keeps its last-active timestamp.
	got, ok, err := SessionByID(ctx, db, stale)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, core.SessionExpired, got.Status)
	require.WithinDuration(t, now.Add(-2*time.Hour), got.UpdatedAt, time.Second,
		"updated_at is preserved as the last-alive time")

	// The live, borderline, and already-completed sessions are untouched.
	for id, want := range map[string]core.SessionStatus{
		live: core.SessionActive, borderline: core.SessionActive, done: core.SessionCompleted,
	} {
		s, ok, err := SessionByID(ctx, db, id)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, want, s.Status)
	}

	// Idempotent: a second pass finds nothing new (the reaped row is no longer active).
	again, err := ExpireStaleSessions(ctx, db, cutoff)
	require.NoError(t, err)
	require.Empty(t, again)
}

func TestTouchSession(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	old := time.Now().UTC().Add(-2 * time.Hour)

	activeID, err := core.NewID()
	require.NoError(t, err)
	require.NoError(t, CreateSession(ctx, db, core.Session{
		ID: activeID, Name: "cc/beat", ProjectSlug: "seamless", Status: core.SessionActive,
		CreatedAt: old, UpdatedAt: old,
	}))
	doneID, err := core.NewID()
	require.NoError(t, err)
	require.NoError(t, CreateSession(ctx, db, core.Session{
		ID: doneID, Name: "cc/gone", ProjectSlug: "seamless", Status: core.SessionCompleted,
		CreatedAt: old, UpdatedAt: old,
	}))

	beat := time.Now().UTC()
	require.NoError(t, TouchSession(ctx, db, activeID, beat))
	require.NoError(t, TouchSessionByName(ctx, db, "cc/beat", beat)) // by-name path

	got, _, err := SessionByID(ctx, db, activeID)
	require.NoError(t, err)
	require.WithinDuration(t, beat, got.UpdatedAt, time.Second, "active session heartbeated forward")

	// Touching a completed session is a no-op: the active-only guard protects it
	// from being resurrected as live.
	require.NoError(t, TouchSession(ctx, db, doneID, beat))
	done, _, err := SessionByID(ctx, db, doneID)
	require.NoError(t, err)
	require.WithinDuration(t, old, done.UpdatedAt, time.Second, "completed session not touched")
	require.Equal(t, core.SessionCompleted, done.Status)

	// Empty id/name are safe no-ops.
	require.NoError(t, TouchSession(ctx, db, "", beat))
	require.NoError(t, TouchSessionByName(ctx, db, "", beat))
}
