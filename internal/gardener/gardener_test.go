package gardener

import (
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/store"
)

// fakeEmbedder returns a one-hot unit vector along an axis encoded in the text as
// "@axis:N@". Two memories with the same axis embed identically (cosine 1.0);
// different axes are orthogonal (cosine 0). This lets a test force a duplicate.
type fakeEmbedder struct{}

func (fakeEmbedder) Model() string { return "fake-model" }

func (fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	axis := 0
	if _, rest, ok := strings.Cut(text, "@axis:"); ok {
		if val, _, ok := strings.Cut(rest, "@"); ok {
			axis, _ = strconv.Atoi(val)
		}
	}
	vec := make([]float32, 16)
	if axis >= 0 && axis < len(vec) {
		vec[axis] = 1
	}
	return vec, nil
}

// fakeChat returns a fixed completion, standing in for the digest summarizer.
type fakeChat struct{ out string }

func (f fakeChat) Model() string { return "fake-chat" }
func (f fakeChat) Complete(_ context.Context, _, _ string) (string, error) {
	return f.out, nil
}

func writeMem(t *testing.T, mgr *files.Manager, name, project, axis string, kind core.MemoryKind, updated time.Time, body string) {
	t.Helper()
	_, err := mgr.WriteMemory(context.Background(), core.Memory{
		ID: mustID(t), Kind: kind, Name: name, Description: name + " description",
		Project: project, Body: "@axis:" + axis + "@\n\n" + body,
		Created: updated, Updated: updated, ValidFrom: updated,
	})
	require.NoError(t, err)
}

func mustID(t *testing.T) string {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	return id
}

func TestRunOnce_ProducesAllThreeProposalKinds(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mgr, err := files.NewManager(dir, db, slog.Default())
	require.NoError(t, err)
	mgr.SetEmbedder(fakeEmbedder{})
	defer func() { _ = mgr.Close() }()

	now := time.Now().UTC()

	// Dedup: two near-identical memories (same embedding axis) -> one merge.
	writeMem(t, mgr, "dup-a", "", "1", core.KindGotcha, now.Add(-2*time.Hour), "first copy")
	writeMem(t, mgr, "dup-b", "", "1", core.KindGotcha, now.Add(-1*time.Hour), "second copy")
	// A distinct, fresh memory: neither duplicate nor stale.
	writeMem(t, mgr, "unique-fresh", "", "3", core.KindReference, now, "unrelated")
	// Staleness: old, distinct, unreferenced, archivable kind -> one archive.
	writeMem(t, mgr, "ancient", "", "4", core.KindGotcha, now.Add(-120*24*time.Hour), "long unread")
	// A constraint just as old must NOT be archived (protected kind).
	writeMem(t, mgr, "old-rule", "", "5", core.KindConstraint, now.Add(-200*24*time.Hour), "still binding")
	// A referenced old memory must NOT be archived (something links to it).
	writeMem(t, mgr, "referenced-old", "", "6", core.KindGotcha, now.Add(-200*24*time.Hour), "keep me")
	writeMem(t, mgr, "linker", "", "7", core.KindReference, now, "see [[referenced-old]] for context")

	// Digest: two completed sessions with findings in one project -> one digest.
	for i := range 2 {
		require.NoError(t, store.CreateSession(ctx, db, core.Session{
			ID: mustID(t), Name: "cc/sess" + strconv.Itoa(i), ProjectSlug: "proj-x",
			Status: core.SessionCompleted, Findings: "finding number " + strconv.Itoa(i),
			CreatedAt: now, UpdatedAt: now,
		}))
	}

	rec := events.NewRecorder(db)
	g := New(db, mgr, fakeEmbedder{}, fakeChat{out: "## Digest\n- did things"}, rec,
		Config{DedupThreshold: 0.88, StalenessDays: 90, DigestDays: 30}, slog.Default())

	res, err := g.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Merges, "one duplicate pair")
	require.Equal(t, 1, res.Archives, "one stale unreferenced memory")
	require.Equal(t, 1, res.Digests, "one project digest")

	// Verify the proposals landed with the right kinds.
	merges, err := store.PendingProposals(ctx, db, store.ProposalMerge)
	require.NoError(t, err)
	require.Len(t, merges, 1)
	archives, err := store.PendingProposals(ctx, db, store.ProposalArchive)
	require.NoError(t, err)
	require.Len(t, archives, 1)
	require.Equal(t, "ancient", archives[0].Payload["name"], "only the unreferenced, non-constraint stale memory")
	digests, err := store.PendingProposals(ctx, db, store.ProposalDigest)
	require.NoError(t, err)
	require.Len(t, digests, 1)
	require.Equal(t, "proj-x", digests[0].Payload["project"])

	// Idempotent: a second pass re-proposes nothing (all keys already exist).
	res2, err := g.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, res2.Total(), "already-proposed items are not raised again")
}

func TestRunOnce_DegradesWithoutEmbedderOrChat(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mgr, err := files.NewManager(dir, db, slog.Default())
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()

	// No embedder, no chat: dedup and digest are skipped; staleness still runs.
	now := time.Now().UTC()
	mgr.SetEmbedder(fakeEmbedder{})
	writeMem(t, mgr, "ancient", "", "2", core.KindGotcha, now.Add(-120*24*time.Hour), "old")

	g := New(db, mgr, nil, nil, nil, Config{}, slog.Default())
	res, err := g.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, res.Merges)
	require.Equal(t, 0, res.Digests)
	require.Equal(t, 1, res.Archives)
}
