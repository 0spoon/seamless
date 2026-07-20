package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

func TestAmbientSessionExternalIdentity_IsolatesClientsAndMutations(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC().Add(-time.Hour)
	externalID := "shared-external-id"

	create := func(id, name, client string) core.Session {
		t.Helper()
		sess := core.Session{
			ID: id, Name: name, ProjectSlug: client, Status: core.SessionActive,
			ExternalSessionID: externalID, ExternalClient: client, Ambient: true,
			CreatedAt: now, UpdatedAt: now,
		}
		require.NoError(t, CreateSession(ctx, db, sess))
		return sess
	}
	claude := create("01IDENTITYCLAUDE0000000000", "cc/shared00-old", "claude-code")
	codex := create("01IDENTITYCODEX00000000000", "cx/shared00-new", "codex")

	gotClaude, ok, err := AmbientSessionByExternalIdentity(ctx, db, "claude-code", externalID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, claude.ID, gotClaude.ID)
	gotCodex, ok, err := AmbientSessionByExternalIdentity(ctx, db, "codex", externalID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, codex.ID, gotCodex.ID)

	beat := time.Now().UTC()
	require.NoError(t, TouchAmbientSession(ctx, db, "codex", externalID, beat))
	require.NoError(t, SetAmbientSessionModel(ctx, db, "codex", externalID, "gpt-5.5"))
	updated, err := UpdateAmbientFindings(ctx, db, "codex", externalID, "codex findings", beat)
	require.NoError(t, err)
	require.True(t, updated)

	gotClaude, _, err = SessionByID(ctx, db, claude.ID)
	require.NoError(t, err)
	require.Empty(t, gotClaude.Model)
	require.Empty(t, gotClaude.Findings)
	require.WithinDuration(t, now, gotClaude.UpdatedAt, time.Second)
	gotCodex, _, err = SessionByID(ctx, db, codex.ID)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.5", gotCodex.Model)
	require.Equal(t, "codex findings", gotCodex.Findings)
	require.WithinDuration(t, beat, gotCodex.UpdatedAt, time.Second)
}

func TestCreateSession_AmbientIdentityConflictIsDistinctFromNameConflict(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC()
	base := core.Session{
		ID: "01IDENTITYBASE000000000000", Name: "cx/prefix00-digest1",
		Status: core.SessionActive, ExternalSessionID: "full-id", ExternalClient: "codex",
		Ambient: true, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, CreateSession(ctx, db, base))

	identityConflict := base
	identityConflict.ID = "01IDENTITYDUPE000000000000"
	identityConflict.Name = "cx/another0-digest2"
	err := CreateSession(ctx, db, identityConflict)
	require.ErrorIs(t, err, ErrSessionIdentityExists)
	require.False(t, errors.Is(err, ErrSessionNameExists))

	nameConflict := base
	nameConflict.ID = "01IDENTITYNAME000000000000"
	nameConflict.ExternalSessionID = "different-full-id"
	err = CreateSession(ctx, db, nameConflict)
	require.ErrorIs(t, err, ErrSessionNameExists)
	require.False(t, errors.Is(err, ErrSessionIdentityExists))
}

func TestMigrationSessionExternalIdentity_BackfillsLegacyRows(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	var pre []Migration
	for _, migration := range migrationList() {
		if migration.Version < 10 {
			pre = append(pre, migration)
		}
	}
	require.NoError(t, migrate(db, pre))

	now := core.FormatTime(time.Now().UTC())
	insert := func(id, name, externalID string, ambient int) {
		t.Helper()
		_, insertErr := db.Exec(`INSERT INTO sessions
			(id, name, status, claude_session_id, ambient, created_at, updated_at)
			VALUES (?, ?, 'active', ?, ?, ?, ?)`, id, name, externalID, ambient, now, now)
		require.NoError(t, insertErr)
	}
	insert("legacy-cc", "cc/abcdef12", "claude-full", 1)
	insert("legacy-cx", "cx/019f7291", "codex-full", 1)
	insert("linked-cc", "sess/linked", "claude-full", 0)
	insert("shared-cc", "cc/shared00", "shared-full", 1)
	insert("shared-cx", "cx/shared00", "shared-full", 1)
	insert("linked-ambiguous", "sess/ambiguous", "shared-full", 0)

	require.NoError(t, migrate(db, migrationList()))

	assertClient := func(name, want string, msgAndArgs ...any) {
		t.Helper()
		var got string
		require.NoError(t, db.QueryRow(
			`SELECT external_client FROM sessions WHERE name = ?`, name).Scan(&got))
		require.Equal(t, want, got, msgAndArgs...)
	}
	assertClient("cc/abcdef12", "claude-code")
	assertClient("cx/019f7291", "codex")
	assertClient("sess/linked", "claude-code")
	assertClient("sess/ambiguous", "", "a cross-client id must not be guessed")

	legacy, ok, err := AmbientSessionByExternalIdentity(
		context.Background(), db, "codex", "codex-full")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "cx/019f7291", legacy.Name, "migration preserves the legacy display handle")
}
