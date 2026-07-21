package gardener

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/store"
)

func TestNormalizeMissQuery(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"lowercases and sorts", "Chroma BOOT race", "boot-chroma-race"},
		{"word order washes out", "race boot chroma", "boot-chroma-race"},
		{"punctuation splits", "chroma/boot: race?", "boot-chroma-race"},
		{"stopwords drop", "how does the chroma boot race work", "boot-chroma-race-work"},
		{"short tokens drop", "go db io chroma", "chroma"},
		{"duplicate terms dedupe", "chroma chroma boot", "boot-chroma"},
		{"digits survive", "port 8081 collision", "8081-collision-port"},
		{"only stopwords is empty", "how does this work", "work"},
		{"nothing usable is empty", "how is a to?", ""},
		{"empty in empty out", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, normalizeMissQuery(tc.in))
		})
	}
}

// newWantedFixture builds a gardener over a fresh store with a pinned clock and
// returns the service plus a recorder for seeding miss events.
func newWantedFixture(t *testing.T, now time.Time) (*Service, *files.Manager, *events.Recorder) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mgr, err := files.NewManager(dir, db, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	rec := events.NewRecorder(db)
	g := New(db, mgr, nil, nil, rec, Config{}, slog.Default())
	g.now = func() time.Time { return now }
	return g, mgr, rec
}

func recordMiss(t *testing.T, rec *events.Recorder, ts time.Time, session, project, query string) {
	t.Helper()
	_, err := rec.Record(context.Background(), core.Event{
		TS: ts, Kind: core.EventRecallMiss, SessionID: session, ProjectSlug: project,
		Payload: map[string]any{"query": query, "source": "recall"},
	})
	require.NoError(t, err)
}

func pendingWanted(t *testing.T, g *Service) []store.Proposal {
	t.Helper()
	props, err := store.PendingProposals(context.Background(), g.db, store.ProposalMemoryWanted)
	require.NoError(t, err)
	return props
}

func TestProposeMemoryWanted_SessionFloor(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	g, _, rec := newWantedFixture(t, now)

	// Same signature from two sessions, phrased differently -> fires.
	recordMiss(t, rec, now.Add(-48*time.Hour), "S1", "proj", "zeta protocol handshake")
	recordMiss(t, rec, now.Add(-24*time.Hour), "S2", "proj", "handshake zeta protocol")
	// One session hammering another signature -> stays quiet.
	recordMiss(t, rec, now.Add(-3*time.Hour), "S1", "proj", "quorum election timeout")
	recordMiss(t, rec, now.Add(-2*time.Hour), "S1", "proj", "quorum election timeout")
	// Unattributed misses bucket as one pseudo-session -> stays quiet.
	recordMiss(t, rec, now.Add(-5*time.Hour), "", "proj", "vector drift metric")
	recordMiss(t, rec, now.Add(-4*time.Hour), "", "proj", "vector drift metric")
	// Outside the window -> invisible even with two sessions.
	recordMiss(t, rec, now.Add(-15*24*time.Hour), "S1", "proj", "ancient lore topic")
	recordMiss(t, rec, now.Add(-16*24*time.Hour), "S2", "proj", "ancient lore topic")

	ctx := context.Background()
	n, err := g.proposeMemoryWanted(ctx, seenKeys(t, ctx, g.db))
	require.NoError(t, err)
	require.Equal(t, 1, n)

	props := pendingWanted(t, g)
	require.Len(t, props, 1)
	p := props[0].Payload
	require.Equal(t, "memory_wanted:proj:handshake-protocol-zeta", p["key"])
	require.Equal(t, "proj", p["project"])
	require.Equal(t, float64(2), p["miss_count"])
	require.Equal(t, float64(2), p["session_count"])
	require.Equal(t, "handshake zeta protocol", p["suggested_title"], "latest phrasing suggests the title")
	queries := payloadStrings(p, "queries")
	require.Equal(t, []string{"handshake zeta protocol", "zeta protocol handshake"}, queries, "recent first")
}

func TestProposeMemoryWanted_DismissedKeyHolds(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	g, _, rec := newWantedFixture(t, now)
	ctx := context.Background()

	recordMiss(t, rec, now.Add(-48*time.Hour), "S1", "proj", "zeta protocol handshake")
	recordMiss(t, rec, now.Add(-24*time.Hour), "S2", "proj", "zeta protocol handshake")

	n, err := g.proposeMemoryWanted(ctx, seenKeys(t, ctx, g.db))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	props := pendingWanted(t, g)
	require.Len(t, props, 1)
	require.NoError(t, g.Dismiss(ctx, props[0].ID))

	// More sessions pile on after the dismissal; the key must hold anyway.
	recordMiss(t, rec, now.Add(-2*time.Hour), "S3", "proj", "handshake zeta protocol")
	n, err = g.proposeMemoryWanted(ctx, seenKeys(t, ctx, g.db))
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, pendingWanted(t, g))
}

func TestProposeMemoryWanted_HitSignatureSuppression(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	g, _, rec := newWantedFixture(t, now)
	ctx := context.Background()

	recordMiss(t, rec, now.Add(-48*time.Hour), "S1", "proj", "zeta protocol handshake")
	recordMiss(t, rec, now.Add(-24*time.Hour), "S2", "proj", "zeta protocol handshake")
	// The same signature also succeeded in the window: intermittent, not a gap.
	_, err := rec.Record(ctx, core.Event{
		TS: now.Add(-12 * time.Hour), Kind: core.EventInjected, SessionID: "S3", ProjectSlug: "proj",
		Payload: map[string]any{"query": "handshake zeta protocol", "item_ids": []string{"01X"}, "source": "recall"},
	})
	require.NoError(t, err)

	n, err := g.proposeMemoryWanted(ctx, seenKeys(t, ctx, g.db))
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestProposeMemoryWanted_LivenessGuard(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	g, mgr, rec := newWantedFixture(t, now)

	recordMiss(t, rec, now.Add(-48*time.Hour), "S1", "proj", "zeta protocol handshake")
	recordMiss(t, rec, now.Add(-24*time.Hour), "S2", "proj", "zeta protocol handshake")
	// The gap has since been filled: an FTS-matching memory exists in scope.
	writeMem(t, mgr, "zeta-handshake", "proj", "1", core.KindGotcha, now.Add(-time.Hour),
		"the zeta protocol handshake sequence and its retry rules")

	ctx := context.Background()
	n, err := g.proposeMemoryWanted(ctx, seenKeys(t, ctx, g.db))
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestProposeMemoryWanted_PerRunCap(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	g, _, rec := newWantedFixture(t, now)

	topics := []string{
		"alpha widget calibration", "bravo widget calibration", "charlie widget calibration",
		"delta widget calibration", "echo widget calibration", "foxtrot widget calibration",
		"golf widget calibration",
	}
	for _, topic := range topics {
		recordMiss(t, rec, now.Add(-48*time.Hour), "S1", "proj", topic)
		recordMiss(t, rec, now.Add(-24*time.Hour), "S2", "proj", topic)
	}

	ctx := context.Background()
	n, err := g.proposeMemoryWanted(ctx, seenKeys(t, ctx, g.db))
	require.NoError(t, err)
	require.Equal(t, memoryWantedMaxPerRun, n)
	require.Len(t, pendingWanted(t, g), memoryWantedMaxPerRun)
}

func TestApplyMemoryWanted_OpensTaskOnce(t *testing.T) {
	g, _, cx := newApplyFixture(t)
	ctx := cx()

	payload := map[string]any{
		"key": "memory_wanted:proj:handshake-protocol-zeta", "project": "proj",
		"signature":  "handshake-protocol-zeta",
		"queries":    []string{"handshake zeta protocol", "zeta protocol handshake"},
		"miss_count": 2, "session_count": 2,
		"suggested_title": "handshake zeta protocol",
		"reason":          "knowledge gap: recall found nothing 2x across 2 sessions in 14d",
	}
	p, err := store.CreateProposal(ctx, g.db, store.ProposalMemoryWanted, payload)
	require.NoError(t, err)

	res, err := g.Apply(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, "applied", res["status"])
	taskID, ok := res["task_id"].(string)
	require.True(t, ok)

	task, err := store.TaskByID(ctx, g.db, taskID)
	require.NoError(t, err)
	require.Equal(t, core.TaskOpen, task.Status)
	require.Equal(t, "gardener", task.CreatedBy)
	require.Equal(t, "proj", task.ProjectSlug)
	require.Equal(t, "Write a memory: handshake zeta protocol", task.Title)
	require.Contains(t, task.Body, `"zeta protocol handshake"`)
	require.Contains(t, task.Body, "knowledge gap")

	got, ok, err := store.ProposalByID(ctx, g.db, p.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, store.ProposalApplied, got.Status)

	// A retry with the same topic (a second proposal, or a re-apply after a
	// partial failure) reuses the identically-titled open task.
	p2, err := store.CreateProposal(ctx, g.db, store.ProposalMemoryWanted, payload)
	require.NoError(t, err)
	res2, err := g.Apply(ctx, p2.ID)
	require.NoError(t, err)
	require.Equal(t, taskID, res2["task_id"])
	require.Equal(t, true, res2["reused"])
}

func TestApplyMemoryWanted_MissingTitleFails(t *testing.T) {
	g, _, cx := newApplyFixture(t)
	ctx := cx()
	p, err := store.CreateProposal(ctx, g.db, store.ProposalMemoryWanted, map[string]any{
		"key": "memory_wanted:proj:x", "project": "proj",
	})
	require.NoError(t, err)
	_, err = g.Apply(ctx, p.ID)
	require.Error(t, err)
	got, ok, err := store.ProposalByID(ctx, g.db, p.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, store.ProposalPending, got.Status, "failed apply leaves the proposal pending")
}

func TestProposeMemoryWanted_PartialTermOverlapDoesNotSuppress(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	g, mgr, rec := newWantedFixture(t, now)
	ctx := context.Background()

	recordMiss(t, rec, now.Add(-48*time.Hour), "S1", "proj", "zeta protocol handshake")
	recordMiss(t, rec, now.Add(-24*time.Hour), "S2", "proj", "zeta protocol handshake")
	// A memory sharing only one common term is not coverage of the gap: the
	// liveness probe is AND-of-all-terms, so this must not suppress.
	writeMem(t, mgr, "unrelated-protocol", "proj", "1", core.KindGotcha, now.Add(-time.Hour),
		"notes about the http protocol upgrade path")

	n, err := g.proposeMemoryWanted(ctx, seenKeys(t, ctx, g.db))
	require.NoError(t, err)
	require.Equal(t, 1, n)
}
