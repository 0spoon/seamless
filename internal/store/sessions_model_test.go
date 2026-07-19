package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// The model set at creation round-trips through every session read path.
func TestSessionModel_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	s := newSession("01MAAAAAAAAAAAAAAAAAAAAAAA", "cc/model1")
	s.Model = "claude-fable-5"
	require.NoError(t, CreateSession(ctx, db, s))

	got, ok, err := SessionByID(ctx, db, s.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "claude-fable-5", got.Model)
}

// SetSessionModel / SetSessionModelByName update an active session in place --
// the mid-session model-switch path -- and are no-ops on empty inputs.
func TestSetSessionModel_UpdatesActive(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	s := newSession("01MBBBBBBBBBBBBBBBBBBBBBBB", "cc/model2")
	require.NoError(t, CreateSession(ctx, db, s))

	require.NoError(t, SetSessionModel(ctx, db, s.ID, "claude-fable-5"))
	got, _, err := SessionByID(ctx, db, s.ID)
	require.NoError(t, err)
	require.Equal(t, "claude-fable-5", got.Model)

	require.NoError(t, SetSessionModelByName(ctx, db, s.Name, "claude-opus-4-8"))
	got, _, err = SessionByID(ctx, db, s.ID)
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-8", got.Model)

	// Empty id/name/model are silent no-ops: an agent that never learns its
	// model is not an error, and "" must never erase a known attribution.
	require.NoError(t, SetSessionModel(ctx, db, "", "gpt-5.5"))
	require.NoError(t, SetSessionModel(ctx, db, s.ID, ""))
	require.NoError(t, SetSessionModelByName(ctx, db, s.Name, ""))
	got, _, err = SessionByID(ctx, db, s.ID)
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-8", got.Model)
}

// A completed/expired session keeps the model it ended with: the setters guard
// on active so late hook traffic cannot rewrite a closed session's attribution.
func TestSetSessionModel_FrozenWhenNotActive(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	s := newSession("01MCCCCCCCCCCCCCCCCCCCCCCC", "cc/model3")
	s.Model = "claude-fable-5"
	require.NoError(t, CreateSession(ctx, db, s))

	s.Status = core.SessionCompleted
	s.UpdatedAt = time.Now().UTC()
	require.NoError(t, UpdateSession(ctx, db, s))

	require.NoError(t, SetSessionModel(ctx, db, s.ID, "gpt-5.5"))
	require.NoError(t, SetSessionModelByName(ctx, db, s.Name, "gpt-5.5"))

	got, _, err := SessionByID(ctx, db, s.ID)
	require.NoError(t, err)
	require.Equal(t, "claude-fable-5", got.Model)
}
