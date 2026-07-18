package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// UpdateAmbientFindingsByName is a targeted, active-ambient-only findings upsert:
// it converges on the latest write, heartbeats updated_at, and refuses to touch a
// completed/expired session or an explicit (non-ambient) one.
func TestUpdateAmbientFindingsByName(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC()

	require.NoError(t, CreateSession(ctx, db, core.Session{
		ID: "01A", Name: "cx/019f7291", ProjectSlug: "seam", Status: core.SessionActive,
		Ambient: true, CreatedAt: now, UpdatedAt: now,
	}))

	// First harvest lands and bumps updated_at.
	later := now.Add(time.Minute)
	updated, err := UpdateAmbientFindingsByName(ctx, db, "cx/019f7291", "(auto-harvested) first", later)
	require.NoError(t, err)
	require.True(t, updated)
	s, ok, err := SessionByName(ctx, db, "cx/019f7291")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "(auto-harvested) first", s.Findings)
	require.True(t, s.UpdatedAt.After(now), "the harvest heartbeats updated_at")

	// A later harvest overwrites (converges on the latest turn).
	updated, err = UpdateAmbientFindingsByName(ctx, db, "cx/019f7291", "(auto-harvested) second", later)
	require.NoError(t, err)
	require.True(t, updated)
	s, _, _ = SessionByName(ctx, db, "cx/019f7291")
	require.Equal(t, "(auto-harvested) second", s.Findings)

	// Empty name or empty findings is a no-op; the prior harvest survives.
	for _, tc := range []struct{ name, findings string }{{"", "x"}, {"cx/019f7291", ""}} {
		updated, err = UpdateAmbientFindingsByName(ctx, db, tc.name, tc.findings, later)
		require.NoError(t, err)
		require.False(t, updated)
	}
	s, _, _ = SessionByName(ctx, db, "cx/019f7291")
	require.Equal(t, "(auto-harvested) second", s.Findings, "no-op writes leave findings intact")

	// A completed session is off-limits (active-only guard).
	require.NoError(t, CreateSession(ctx, db, core.Session{
		ID: "01B", Name: "cx/done0000", ProjectSlug: "seam", Status: core.SessionCompleted,
		Ambient: true, CreatedAt: now, UpdatedAt: now,
	}))
	updated, err = UpdateAmbientFindingsByName(ctx, db, "cx/done0000", "nope", later)
	require.NoError(t, err)
	require.False(t, updated, "a completed session is not harvestable")

	// An explicit (non-ambient) active session is off-limits (its findings are the
	// agent's own via session_update).
	require.NoError(t, CreateSession(ctx, db, core.Session{
		ID: "01C", Name: "sess/work", ProjectSlug: "seam", Status: core.SessionActive,
		Ambient: false, CreatedAt: now, UpdatedAt: now,
	}))
	updated, err = UpdateAmbientFindingsByName(ctx, db, "sess/work", "nope", later)
	require.NoError(t, err)
	require.False(t, updated, "an explicit session is not ambient-harvestable")
}

// RecentFindings surfaces a reaper-expired ambient session's findings -- Codex's
// only end state -- while keeping every non-Codex exclusion: a blank finding, an
// expired EXPLICIT session, and a completed session still behave as before.
func TestRecentFindings_SurfacesExpiredAmbient(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC()

	mk := func(id, name string, status core.SessionStatus, ambient bool, findings string, ago time.Duration) {
		require.NoError(t, CreateSession(ctx, db, core.Session{
			ID: id, Name: name, ProjectSlug: "seam", Status: status, Ambient: ambient,
			Findings: findings, CreatedAt: now.Add(-ago), UpdatedAt: now.Add(-ago),
		}))
	}

	mk("01A", "cc/done0000", core.SessionCompleted, true, "cc completed", 5*time.Minute)         // shown (baseline)
	mk("01B", "cx/019f7291", core.SessionExpired, true, "(auto-harvested) codex", 1*time.Minute) // shown (new)
	mk("01C", "cx/empty000", core.SessionExpired, true, "", 2*time.Minute)                       // excluded: blank
	mk("01D", "cx/nosum0000", core.SessionExpired, true, core.FindingNoSummary, 2*time.Minute)   // excluded: sentinel
	mk("01E", "sess/crash", core.SessionExpired, false, "explicit interim", 3*time.Minute)       // excluded: not ambient

	rf, err := RecentFindings(ctx, db, "seam", 10)
	require.NoError(t, err)

	got := make([]string, len(rf))
	for i, s := range rf {
		got[i] = s.Findings
	}
	// Newest-first: the codex expired session, then the completed one.
	require.Equal(t, []string{"(auto-harvested) codex", "cc completed"}, got,
		"expired ambient findings surface; blank/sentinel/explicit-expired do not")

	// SiblingFindings applies the same rule across projects.
	sf, err := SiblingFindings(ctx, db, []string{"seam"}, 10)
	require.NoError(t, err)
	sgot := make([]string, len(sf))
	for i, s := range sf {
		sgot[i] = s.Findings
	}
	require.Equal(t, []string{"(auto-harvested) codex", "cc completed"}, sgot)
}
