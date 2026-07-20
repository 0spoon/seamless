package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

func TestReactivateAmbientSession(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	created := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, CreateSession(ctx, db, core.Session{
		ID: "01REACT000000000000000000A", Name: "cc/react001", ProjectSlug: "demo",
		Status: core.SessionCompleted, Findings: "harvested findings",
		ExternalSessionID: "react001-full", ExternalClient: "claude-code", Ambient: true,
		Metadata:  map[string]any{"cwd": "/work/demo"},
		CreatedAt: created, UpdatedAt: created,
	}))

	// Missing identity: found=false, no error.
	_, found, err := ReactivateAmbientSession(
		ctx, db, "claude-code", "missing-full", "", time.Now().UTC())
	require.NoError(t, err)
	require.False(t, found)

	// Empty project keeps the existing scope; findings and metadata are untouched;
	// status flips back to active and updated_at is bumped.
	now := time.Now().UTC()
	resumed, found, err := ReactivateAmbientSession(
		ctx, db, "claude-code", "react001-full", "", now)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "cc/react001", resumed.Name, "legacy display names are preserved")
	sess, ok, err := SessionByName(ctx, db, "cc/react001")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, core.SessionActive, sess.Status)
	require.Equal(t, "demo", sess.ProjectSlug, "empty project must keep the existing scope")
	require.Equal(t, "harvested findings", sess.Findings, "reactivation must never touch findings")
	require.Equal(t, "/work/demo", sess.Metadata["cwd"], "reactivation must never touch metadata")
	require.True(t, sess.UpdatedAt.After(created), "reactivation bumps recency")

	// A non-empty project re-scopes.
	_, found, err = ReactivateAmbientSession(
		ctx, db, "claude-code", "react001-full", "other", time.Now().UTC())
	require.NoError(t, err)
	require.True(t, found)
	sess, _, err = SessionByName(ctx, db, "cc/react001")
	require.NoError(t, err)
	require.Equal(t, "other", sess.ProjectSlug)
}

func TestActiveSessionIDs(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seed := func(id, name string, status core.SessionStatus) {
		require.NoError(t, CreateSession(ctx, db, core.Session{
			ID: id, Name: name, Status: status, CreatedAt: now, UpdatedAt: now,
		}))
	}
	seed("01ACTIVE00000000000000000A", "s/a", core.SessionActive)
	seed("01ACTIVE00000000000000000B", "s/b", core.SessionCompleted)
	seed("01ACTIVE00000000000000000C", "s/c", core.SessionExpired)

	got, err := ActiveSessionIDs(ctx, db, []string{
		"01ACTIVE00000000000000000A",
		"01ACTIVE00000000000000000B",
		"01ACTIVE00000000000000000C",
		"01ACTIVE00000000000000000X", // unknown
	})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"01ACTIVE00000000000000000A": true}, got)

	// No ids: empty result, no query.
	got, err = ActiveSessionIDs(ctx, db, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}
