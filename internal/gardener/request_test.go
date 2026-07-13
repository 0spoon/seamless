package gardener

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/llm"
	"github.com/0spoon/seamless/internal/store"
)

// newRequestGardener builds a gardener over a fresh store with the given chat
// output (empty => no chat client at all), and seeds three active global
// memories in a known newest-first order: [1] keep-me, [2] drop-me,
// [3] stale-thing.
func newRequestGardener(t *testing.T, chatOut string) (context.Context, *sql.DB, *Service) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mgr, err := files.NewManager(dir, db, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })

	now := time.Now().UTC()
	writeMem(t, mgr, "keep-me", "", "1", core.KindGotcha, now, "canonical")
	writeMem(t, mgr, "drop-me", "", "2", core.KindGotcha, now.Add(-1*time.Hour), "redundant")
	writeMem(t, mgr, "stale-thing", "", "3", core.KindReference, now.Add(-2*time.Hour), "obsolete")

	// A genuine nil llm.Chat interface (not a typed-nil pointer) when no chat.
	var chat llm.Chat
	if chatOut != "" {
		chat = fakeChat{out: chatOut}
	}
	g := New(db, mgr, nil, chat, events.NewRecorder(db), Config{}, slog.Default())
	return ctx, db, g
}

func TestRequest_CreatesMergeAndArchive(t *testing.T) {
	ctx, db, g := newRequestGardener(t,
		`{"ops":[{"op":"merge","keep":1,"drop":2},{"op":"archive","target":3,"reason":"obsolete"}]}`)

	res, err := g.Request(ctx, "keep-me and drop-me are duplicates; retire stale-thing", "")
	require.NoError(t, err)
	require.Equal(t, 2, res.Total)
	require.Equal(t, 1, res.ByKind[store.ProposalMerge])
	require.Equal(t, 1, res.ByKind[store.ProposalArchive])
	require.Len(t, res.Created, 2)
	require.Empty(t, res.Skipped)

	merges, err := store.PendingProposals(ctx, db, store.ProposalMerge)
	require.NoError(t, err)
	require.Len(t, merges, 1)
	keep, _ := merges[0].Payload["keep"].(map[string]any)
	drop, _ := merges[0].Payload["drop"].(map[string]any)
	require.Equal(t, "keep-me", keep["name"])
	require.Equal(t, "drop-me", drop["name"])
	require.Equal(t, "request", merges[0].Payload["source"], "request-originated proposals are tagged")

	archives, err := store.PendingProposals(ctx, db, store.ProposalArchive)
	require.NoError(t, err)
	require.Len(t, archives, 1)
	require.Equal(t, "stale-thing", archives[0].Payload["name"])
	require.Equal(t, "obsolete", archives[0].Payload["reason"])
}

func TestRequest_SkipsInvalidOps(t *testing.T) {
	ctx, db, g := newRequestGardener(t,
		`{"ops":[{"op":"merge","keep":1,"drop":1},{"op":"archive","target":99,"reason":"x"},{"op":"delete","target":2}]}`)

	res, err := g.Request(ctx, "do some questionable things", "")
	require.NoError(t, err)
	require.Equal(t, 0, res.Total, "every op is invalid")
	require.Len(t, res.Skipped, 3, "keep==drop, out-of-range, and unknown op are each skipped")

	all, err := store.PendingProposals(ctx, db, "")
	require.NoError(t, err)
	require.Empty(t, all, "no proposal is created from invalid ops")
}

func TestRequest_NoChatIsAnError(t *testing.T) {
	ctx, db, g := newRequestGardener(t, "") // no chat client

	_, err := g.Request(ctx, "merge the duplicates", "")
	require.ErrorIs(t, err, ErrNoChat)

	all, err := store.PendingProposals(ctx, db, "")
	require.NoError(t, err)
	require.Empty(t, all)
}

func TestRequest_EmptyRequestIsAnError(t *testing.T) {
	ctx, _, g := newRequestGardener(t, `{"ops":[]}`)

	_, err := g.Request(ctx, "   ", "")
	require.ErrorIs(t, err, ErrEmptyRequest)
}

func TestRequest_GarbageOutputCreatesNothing(t *testing.T) {
	ctx, db, g := newRequestGardener(t, "I'm sorry, I cannot help with that request.")

	_, err := g.Request(ctx, "merge the duplicates", "")
	require.ErrorIs(t, err, ErrUnparseable)

	all, err := store.PendingProposals(ctx, db, "")
	require.NoError(t, err)
	require.Empty(t, all, "an unparseable completion never fabricates proposals")
}

func TestRequest_EmptyOpsIsCleanNoResult(t *testing.T) {
	ctx, db, g := newRequestGardener(t, "```json\n{\"ops\":[]}\n```")

	res, err := g.Request(ctx, "nothing needs changing", "")
	require.NoError(t, err, "an empty op list is a clean outcome, not an error")
	require.Equal(t, 0, res.Total)
	require.Equal(t, "no proposals matched", res.Summary)

	all, err := store.PendingProposals(ctx, db, "")
	require.NoError(t, err)
	require.Empty(t, all)
}
