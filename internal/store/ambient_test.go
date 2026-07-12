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

	projects, err := ActiveAmbientProjects(ctx, db, ambientWindowForTest)
	require.NoError(t, err)
	// Distinct projects only, ordered by most recent activity: other before demo.
	require.Equal(t, []string{"other", "demo"}, projects)

	// The project-scoped lookup returns that project's latest ambient session.
	sess, ok, err := LatestActiveAmbientSessionForProject(ctx, db, "demo", ambientWindowForTest)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "cc/demo2", sess.Name, "returns the most recently updated ambient in the project")

	// A project with no ambient session yields found=false, not another project's.
	_, ok, err = LatestActiveAmbientSessionForProject(ctx, db, "nope", ambientWindowForTest)
	require.NoError(t, err)
	require.False(t, ok)
}

// ambientWindowForTest mirrors the MCP server's ambientFallbackWindow so the
// store test exercises the same recency boundary.
const ambientWindowForTest = 6 * time.Hour
