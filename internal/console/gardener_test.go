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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/gardener"
	"github.com/0spoon/seamless/internal/store"
)

// stubChat is a canned llm.Chat: it returns a fixed completion regardless of the
// prompt, so the console can exercise the natural-language request path.
type stubChat struct{ out string }

func (c stubChat) Model() string                                     { return "stub-chat" }
func (c stubChat) Complete(context.Context, string, string) (string, error) { return c.out, nil }

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

// newConsoleWithChat wires a chat-backed gardener and seeds two active global
// memories in a known newest-first order ([1] keep-me, [2] drop-me), so the
// natural-language request path has candidates to reference.
func newConsoleWithChat(t *testing.T, chatOut string) (context.Context, *sql.DB, *http.ServeMux) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	dataDir := filepath.Join(dir, "data")
	mgr, err := files.NewManager(dataDir, db, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })

	now := time.Now().UTC()
	writeConsoleMem(t, mgr, "keep-me", now)
	writeConsoleMem(t, mgr, "drop-me", now.Add(-time.Hour))

	rec := events.NewRecorder(db)
	garden := gardener.New(db, mgr, nil, stubChat{out: chatOut}, rec, gardener.FromConfig(config.Gardener{}), slog.Default())
	svc, err := New(Config{DB: db, Files: mgr, Gardener: garden, Events: rec, DataDir: dataDir, APIKey: testKey})
	require.NoError(t, err)
	mux := http.NewServeMux()
	svc.Register(mux)
	return ctx, db, mux
}

func writeConsoleMem(t *testing.T, mgr *files.Manager, name string, updated time.Time) {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	_, err = mgr.WriteMemory(context.Background(), core.Memory{
		ID: id, Kind: core.KindGotcha, Name: name, Description: name + " description",
		Body: "body", Created: updated, Updated: updated, ValidFrom: updated,
	})
	require.NoError(t, err)
}

func postForm(mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return do(mux, req)
}

func TestGardenerRequest_CreatesProposals(t *testing.T) {
	ctx, db, mux := newConsoleWithChat(t, `{"ops":[{"op":"merge","keep":1,"drop":2}]}`)

	rr := postForm(mux, "/console/gardener/request", "request=keep-me+and+drop-me+are+duplicates&project=")
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.True(t, strings.HasPrefix(rr.Header().Get("Location"), "/console/gardener?notice="),
		"a successful request redirects with a positive notice, got %q", rr.Header().Get("Location"))

	merges, err := store.PendingProposals(ctx, db, store.ProposalMerge)
	require.NoError(t, err)
	require.Len(t, merges, 1)
	require.Equal(t, "request", merges[0].Payload["source"], "request-originated proposals are tagged for the UI chip")
}

func TestGardenerRequest_UnavailableWithoutChat(t *testing.T) {
	_, _, mux := newConsoleWithGardener(t) // gardener without a chat client
	var data gardenerData
	getJSON(t, mux, "/console/gardener?format=json", &data)
	require.False(t, data.CanRequest, "the request box is gated off when no chat client is configured")
}
