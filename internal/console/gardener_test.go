package console

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/gardener"
	"github.com/0spoon/seamless/internal/store"
)

// newConsoleWithGardener wires a real gardener (no embedder/chat) so apply/dismiss
// round-trip through the proposal store and files.
func newConsoleWithGardener(t *testing.T) (context.Context, *sql.DB, *http.ServeMux) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	dataDir := filepath.Join(dir, "data")
	mgr, err := files.NewManager(dataDir, db, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	rec := events.NewRecorder(db)
	garden := gardener.New(db, mgr, nil, nil, rec, gardener.FromConfig(config.Gardener{}), slog.Default())

	svc, err := New(Config{DB: db, Files: mgr, Gardener: garden, Events: rec, DataDir: dataDir, APIKey: testKey})
	require.NoError(t, err)
	mux := http.NewServeMux()
	svc.Register(mux)
	return context.Background(), db, mux
}

func post(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	return do(mux, req)
}

func TestGardenerPage_CardsDismissApply(t *testing.T) {
	ctx, db, mux := newConsoleWithGardener(t)

	archiveP, err := store.CreateProposal(ctx, db, store.ProposalArchive, map[string]any{
		"key": "archive:MISSING", "id": "MISSINGMEM", "name": "old-note",
		"project": "seamless", "kind": "reference", "description": "went stale", "reason": "no activity in 90d",
	})
	require.NoError(t, err)
	mergeP, err := store.CreateProposal(ctx, db, store.ProposalMerge, map[string]any{
		"key": "merge:a|b", "score": 0.91,
		"keep": map[string]any{"id": "K", "name": "keep-me", "project": "seamless", "kind": "gotcha", "description": "newer"},
		"drop": map[string]any{"id": "D", "name": "drop-me", "project": "seamless", "kind": "gotcha", "description": "older"},
	})
	require.NoError(t, err)
	digestP, err := store.CreateProposal(ctx, db, store.ProposalDigest, map[string]any{
		"key": "digest:seamless:2026-07", "project": "seamless", "month": "2026-07",
		"session_count": 4.0, "title": "seamless digest 2026-07", "body": "## Findings\n- did the thing",
	})
	require.NoError(t, err)

	// Cards render, one per kind.
	var data gardenerData
	getJSON(t, mux, "/console/gardener?format=json", &data)
	require.Len(t, data.Cards, 3)
	byKind := map[string]proposalCard{}
	for _, c := range data.Cards {
		byKind[c.Kind] = c
	}
	require.Equal(t, "keep-me", byKind["merge"].Keep.Name)
	require.InDelta(t, 0.91, byKind["merge"].Score, 0.001)
	require.Equal(t, 4, byKind["digest"].SessionCount)
	require.Equal(t, "old-note", byKind["archive"].Archive.Name)

	// Dismiss the merge -> gone from pending.
	require.Equal(t, http.StatusSeeOther, post(mux, "/console/gardener/"+mergeP.ID+"/dismiss").Code)
	p, ok, err := store.ProposalByID(ctx, db, mergeP.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, store.ProposalDismissed, p.Status)

	// Apply the digest -> a note is written and the proposal is applied.
	require.Equal(t, http.StatusSeeOther, post(mux, "/console/gardener/"+digestP.ID+"/apply").Code)
	p, _, err = store.ProposalByID(ctx, db, digestP.ID)
	require.NoError(t, err)
	require.Equal(t, store.ProposalApplied, p.Status)

	// Apply the archive whose memory is missing -> flash error, proposal stays pending.
	rr := post(mux, "/console/gardener/"+archiveP.ID+"/apply")
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.True(t, strings.HasPrefix(rr.Header().Get("Location"), "/console/gardener?error="))
	p, _, err = store.ProposalByID(ctx, db, archiveP.ID)
	require.NoError(t, err)
	require.Equal(t, store.ProposalPending, p.Status)
}
