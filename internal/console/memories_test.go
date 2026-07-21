package console

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/store"
)

// newConsoleWithFiles builds a console backed by a real files.Manager over a
// temp data dir, so memory writes/archives round-trip through the source-of-truth
// files. It returns the DB (to seed events), the manager (to seed memories), and
// the mux.
func newConsoleWithFiles(t *testing.T) (*sql.DB, *files.Manager, *http.ServeMux) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	dataDir := filepath.Join(dir, "data")
	mgr, err := files.NewManager(dataDir, db, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })

	svc, err := New(Config{
		DB: db, Files: mgr, Events: events.NewRecorder(db), DataDir: dataDir, APIKey: testKey,
	})
	require.NoError(t, err)
	mux := http.NewServeMux()
	svc.Register(mux)
	return db, mgr, mux
}

func writeMemory(t *testing.T, mgr *files.Manager, kind core.MemoryKind, project, name, desc string) core.Memory {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	now := time.Now().UTC()
	m, err := mgr.WriteMemory(context.Background(), core.Memory{
		ID: id, Kind: kind, Name: name, Description: desc, Project: project,
		Body: "body of " + name, Created: now, Updated: now, ValidFrom: now,
	})
	require.NoError(t, err)
	return m
}

func TestMemoriesPage_GroupsAndArchive(t *testing.T) {
	_, mgr, mux := newConsoleWithFiles(t)

	m1 := writeMemory(t, mgr, core.KindGotcha, "seamless", "watcher-race", "a surprising pitfall")
	writeMemory(t, mgr, core.KindConstraint, "", "no-cgo", "never enable cgo")

	// List
	var data memoriesData
	getJSON(t, mux, "/console/memories?format=json", &data)
	require.Equal(t, 2, data.ActiveCount)
	require.Equal(t, 0, data.InactiveCount)
	// global group ("") sorts first.
	require.Equal(t, "", data.Groups[0].Project)
	require.Equal(t, "seamless", data.Groups[1].Project)

	// HTML renders the memory name.
	reqH := httptest.NewRequest(http.MethodGet, "/console/memories", nil)
	reqH.Header.Set("Authorization", "Bearer "+testKey)
	rrH := do(mux, reqH)
	require.Equal(t, http.StatusOK, rrH.Code)
	require.Contains(t, rrH.Body.String(), "watcher-race")

	// Archive m1
	req := httptest.NewRequest(http.MethodPost, "/console/memories/"+m1.ID+"/archive", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusSeeOther, rr.Code)

	// It is now inactive.
	var after memoriesData
	getJSON(t, mux, "/console/memories?format=json", &after)
	require.Equal(t, 1, after.ActiveCount)
	require.Equal(t, 1, after.InactiveCount)
	require.Equal(t, "archived", after.Inactive[0].Status)
	require.Equal(t, "watcher-race", after.Inactive[0].Name)

	// A historical memory cannot be mistaken for current guidance in the reader.
	page := getPeek(t, mux, "/console/memories/"+m1.ID)
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "This memory is archived.")
	require.Contains(t, page.Body.String(), "It no longer enters agent context.")
}
