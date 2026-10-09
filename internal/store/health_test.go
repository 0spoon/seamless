package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/arctop/seamless/internal/core"
)

func TestGetHealthFacts(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// A fresh installation has no facts: nothing is invented.
	facts, err := GetHealthFacts(ctx, db)
	require.NoError(t, err)
	require.Empty(t, facts.Clients)
	require.Nil(t, facts.LastBriefing)

	now := time.Now().UTC().Truncate(time.Second)
	for _, s := range []struct {
		id, name, client string
		updated          time.Duration
	}{
		{"01S1", "cc/one", "claude-code", 4 * time.Minute},
		{"01S2", "cc/two", "claude-code", 3 * time.Hour},
		{"01S3", "cx/one", "codex", 72 * time.Hour},
		{"01S4", "sess/named", "", time.Minute},           // an explicit session names no client
		{"01S5", "cx/legacy", "", 30 * time.Minute},       // no column value: the ambient prefix names it
		{"01S6", "cc/older-legacy", "", 10 * time.Minute}, // older than cc/one: does not move claude-code
	} {
		require.NoError(t, CreateSession(ctx, db, core.Session{
			ID: s.id, Name: s.name, ExternalClient: s.client, Status: core.SessionActive,
			CreatedAt: now.Add(-s.updated), UpdatedAt: now.Add(-s.updated),
		}))
	}
	insert := func(id, kind, payload string, ago time.Duration) {
		_, err := db.Exec(`INSERT INTO events (id, ts, kind, payload) VALUES (?, ?, ?, ?)`,
			id, core.FormatTime(now.Add(-ago)), kind, payload)
		require.NoError(t, err)
	}
	insert("01E1", "retrieval.injected", `{"hook":"session-start"}`, 2*time.Hour)
	insert("01E2", "retrieval.injected", `{"hook":"session-start"}`, 5*time.Minute)
	insert("01E3", "retrieval.injected", `{"hook":"user-prompt-submit"}`, time.Minute) // recall, not the briefing
	insert("01E4", "memory.read", `{"hook":"session-start"}`, 30*time.Second)          // wrong kind

	facts, err = GetHealthFacts(ctx, db)
	require.NoError(t, err)
	require.Equal(t, []ClientSeen{
		{Client: "claude-code", LastSeen: now.Add(-4 * time.Minute)},
		{Client: "codex", LastSeen: now.Add(-30 * time.Minute)},
	}, facts.Clients, "one row per client, newest activity first; unclassified sessions add none")
	require.NotNil(t, facts.LastBriefing)
	require.Equal(t, "01E2", facts.LastBriefing.EventID)
	require.Equal(t, now.Add(-5*time.Minute), facts.LastBriefing.At)
}

// LatestSessionProject names where the owner's agents worked last: the newest
// session that has a project, skipping ones that do not.
func TestLatestSessionProject(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	project, err := LatestSessionProject(ctx, db)
	require.NoError(t, err)
	require.Empty(t, project, "no session names a project yet")

	now := time.Now().UTC()
	for _, s := range []struct {
		id, project string
		updated     time.Duration
	}{
		{"01L1", "orbital", 3 * time.Hour},
		{"01L2", "homelab", 2 * time.Hour},
		{"01L3", "", time.Minute}, // newer, but in no project
	} {
		require.NoError(t, CreateSession(ctx, db, core.Session{
			ID: s.id, Name: "cc/" + s.id, ProjectSlug: s.project, Status: core.SessionActive,
			CreatedAt: now.Add(-s.updated), UpdatedAt: now.Add(-s.updated),
		}))
	}
	project, err = LatestSessionProject(ctx, db)
	require.NoError(t, err)
	require.Equal(t, "homelab", project)
}
