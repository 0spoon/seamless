package hooks

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/store"
)

// openRaceDB opens a fresh migrated database for the ambient-race tests, without
// the seeded memories/handlers of newHandlerServer (these tests call the handler
// methods directly).
func openRaceDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestEnsureAmbientSession_ResumeDoesNotClobberHarvest is the regression test
// for the resume/harvest write race: ensureAmbientSession used to read the full
// session row and write every mutable field back, so a resume racing a
// transcript harvest could overwrite the harvest's findings with the stale value
// it had read. The resume path is now a targeted UPDATE that never touches
// findings, so the harvester's last write must always survive. Run with -race.
func TestEnsureAmbientSession_ResumeDoesNotClobberHarvest(t *testing.T) {
	db := openRaceDB(t)
	h := NewHandler(Config{DB: db, APIKey: testKey})
	ctx := context.Background()

	payload := hookPayload{SessionID: "racecafe-0001", Source: "startup"}
	require.Equal(t, "cc/racecafe", h.ensureAmbientSession(ctx, ClientClaudeCode, payload))
	sess, ok, err := store.SessionByName(ctx, db, "cc/racecafe")
	require.NoError(t, err)
	require.True(t, ok)

	const iters = 200
	var wg sync.WaitGroup
	wg.Add(2)
	// Harvester: sequential full-row findings writes, exactly what the
	// SessionEnd hook's completeClaudeSessions does.
	go func() {
		defer wg.Done()
		for i := range iters {
			s := sess
			s.Findings = fmt.Sprintf("harvest-%d", i)
			s.UpdatedAt = time.Now().UTC()
			if err := store.UpdateSession(ctx, db, s); err != nil {
				t.Errorf("harvest write %d: %v", i, err)
				return
			}
		}
	}()
	// Resumer: concurrent SessionStart hooks resuming the same ambient session.
	go func() {
		defer wg.Done()
		for i := range iters {
			if got := h.ensureAmbientSession(ctx, ClientClaudeCode, payload); got != "cc/racecafe" {
				t.Errorf("resume %d: got %q, want cc/racecafe", i, got)
				return
			}
		}
	}()
	wg.Wait()

	final, ok, err := store.SessionByName(ctx, db, "cc/racecafe")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, fmt.Sprintf("harvest-%d", iters-1), final.Findings,
		"a racing resume must never clobber the harvest's findings")
}

// TestEnsureAmbientSession_ConcurrentCreateSingleRow stresses the create race:
// several SessionStart hooks for the same Claude session can all miss the resume
// and collide on the UNIQUE session name. Every caller must still get the
// ambient name back (the losers resume the winner's row) and exactly one session
// row may exist. Run with -race.
func TestEnsureAmbientSession_ConcurrentCreateSingleRow(t *testing.T) {
	db := openRaceDB(t)
	h := NewHandler(Config{DB: db, APIKey: testKey})
	ctx := context.Background()

	const n = 8
	payload := hookPayload{SessionID: "fresh000-0001", Source: "startup"}
	results := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = h.ensureAmbientSession(ctx, ClientClaudeCode, payload)
		}(i)
	}
	wg.Wait()

	for i, got := range results {
		require.Equal(t, "cc/fresh000", got, "goroutine %d must resolve the ambient session", i)
	}
	sessions, err := store.ListSessions(ctx, db, "", time.Time{}, 0)
	require.NoError(t, err)
	require.Len(t, sessions, 1, "racing creates must not fork a second ambient row")
}
