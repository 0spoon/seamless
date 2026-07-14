package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

func newSession(id, name string) core.Session {
	now := time.Now().UTC()
	return core.Session{
		ID: id, Name: name, Status: core.SessionActive,
		Source: "test", CreatedAt: now, UpdatedAt: now,
	}
}

// A duplicate session name must surface as ErrSessionNameExists, not a raw
// driver error: sessions.name is UNIQUE and callers race to create the same
// ambient name, so "someone beat me to it" (resume) has to be distinguishable
// from a real database failure without matching on error text.
func TestCreateSession_DuplicateNameIsTyped(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	require.NoError(t, CreateSession(ctx, db, newSession("01AAAAAAAAAAAAAAAAAAAAAAAA", "cc/dupe")))

	err := CreateSession(ctx, db, newSession("01BBBBBBBBBBBBBBBBBBBBBBBB", "cc/dupe"))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSessionNameExists)
	require.Contains(t, err.Error(), "cc/dupe", "the error names the contended session")

	// The winner's row is intact and the loser wrote nothing.
	got, ok, err := SessionByName(ctx, db, "cc/dupe")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "01AAAAAAAAAAAAAAAAAAAAAAAA", got.ID)
}

// Documents the sentinel's stated limit: any uniqueness failure on the insert
// reads as a name collision, including an id (primary key) collision. That is
// sound only because callers mint a fresh ULID per call, so a PK collision
// implies an id-generation bug rather than a contended name. Pinned so the
// conflation is a decision on the record, not a surprise -- if a second unique
// column is ever added to sessions, this test should fail and force the mapping
// to get more precise.
func TestCreateSession_DuplicateIDAlsoReadsAsNameExists(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	require.NoError(t, CreateSession(ctx, db, newSession("01CCCCCCCCCCCCCCCCCCCCCCCC", "cc/one")))

	err := CreateSession(ctx, db, newSession("01CCCCCCCCCCCCCCCCCCCCCCCC", "cc/two"))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSessionNameExists)
}

// Distinct names insert cleanly -- the sentinel must not fire on the happy path.
func TestCreateSession_DistinctNamesOK(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	require.NoError(t, CreateSession(ctx, db, newSession("01DDDDDDDDDDDDDDDDDDDDDDDD", "cc/a")))
	require.NoError(t, CreateSession(ctx, db, newSession("01EEEEEEEEEEEEEEEEEEEEEEEE", "cc/b")))
}
