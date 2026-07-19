package console

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// The library screens (Notes / Memories / Tasks) render one unified page: a
// grouped rail plus a reader. These tests cover the reader-specific behavior;
// the JSON list payloads and ?peek=1 fragments are covered in peek_test.go.

func TestNotesLibrary_AutoSelectsNewest(t *testing.T) {
	_, mgr, mux := newConsoleWithFiles(t)
	writeNote(t, mgr, "seamless", "n-old", "Older Note", "the older body")
	writeNote(t, mgr, "seamless", "n-new", "Newer Note", "the newer body")

	html := getPeek(t, mux, "/console/notes")
	require.Equal(t, http.StatusOK, html.Code)
	body := html.Body.String()
	require.Contains(t, body, `id="lib-reader"`)
	// The newest note is open in the reader (its body renders) and its rail
	// item carries the current marker + the pin URL for the client.
	require.Contains(t, body, "the newer body")
	require.NotContains(t, body, "the older body")
	require.Contains(t, body, `aria-current="page"`)
	require.Contains(t, body, "data-auto-url=")
}

func TestNotesLibrary_DetailPageAndReaderFragment(t *testing.T) {
	_, mgr, mux := newConsoleWithFiles(t)
	n := writeNote(t, mgr, "seamless", "n-doc", "Doc Note", "full document body")
	writeNote(t, mgr, "seamless", "n-other", "Other Note", "other body")

	// The detail URL renders the same library page with this note selected.
	page := getPeek(t, mux, "/console/notes/"+n.ID)
	require.Equal(t, http.StatusOK, page.Code)
	body := page.Body.String()
	require.Contains(t, body, `id="lib-reader"`)
	require.Contains(t, body, "full document body")
	require.Contains(t, body, `id="ri-`+n.ID+`" href="/console/notes/`+n.ID+`"`)
	require.NotContains(t, body, "data-auto-url=", "an explicit selection is not client-pinned")

	// ?reader=1 returns just the reader fragment for the in-place swap.
	frag := getPeek(t, mux, "/console/notes/"+n.ID+"?reader=1")
	require.Equal(t, http.StatusOK, frag.Code)
	require.NotContains(t, frag.Body.String(), "<html")
	require.Contains(t, frag.Body.String(), "reader-sheet")
	require.Contains(t, frag.Body.String(), "full document body")

	// The list filter survives on the detail URL: a bogus sort is still a 400.
	bad := getPeek(t, mux, "/console/notes/"+n.ID+"?sort=bogus")
	require.Equal(t, http.StatusBadRequest, bad.Code)
}

func TestMemoriesLibrary_DetailAndReaderFragment(t *testing.T) {
	_, mgr, mux := newConsoleWithFiles(t)
	m := writeMemory(t, mgr, core.KindGotcha, "seamless", "lib-mem", "a memory description")

	page := getPeek(t, mux, "/console/memories/"+m.ID)
	require.Equal(t, http.StatusOK, page.Code)
	body := page.Body.String()
	require.Contains(t, body, `id="lib-reader"`)
	require.Contains(t, body, "body of lib-mem")
	require.Contains(t, body, `aria-current="page"`)

	frag := getPeek(t, mux, "/console/memories/"+m.ID+"?reader=1")
	require.Equal(t, http.StatusOK, frag.Code)
	require.NotContains(t, frag.Body.String(), "<html")
	require.Contains(t, frag.Body.String(), "body of lib-mem")
	require.Contains(t, frag.Body.String(), "archive", "the reader carries the archive action")
}

func TestMemoriesLibrary_ListAutoSelects(t *testing.T) {
	_, mgr, mux := newConsoleWithFiles(t)
	writeMemory(t, mgr, core.KindConstraint, "", "solo-mem", "only memory")

	html := getPeek(t, mux, "/console/memories")
	require.Equal(t, http.StatusOK, html.Code)
	require.Contains(t, html.Body.String(), "body of solo-mem")
	require.Contains(t, html.Body.String(), "data-auto-url=")
}

func TestTasksLibrary_AutoSelectPrefersInProgress(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mk := func(id, title string, status core.TaskStatus) {
		require.NoError(t, store.CreateTask(ctx, db, core.Task{
			ID: id, ProjectSlug: "seamless", Title: title, Body: "do " + title,
			Status: status, CreatedAt: base, UpdatedAt: base,
		}))
	}
	mk("TR", "ready-task", core.TaskOpen)
	mk("TP", "active-task", core.TaskInProgress)

	html := getPeek(t, mux, "/console/tasks")
	require.Equal(t, http.StatusOK, html.Code)
	body := html.Body.String()
	require.Contains(t, body, `id="lib-reader"`)
	require.Contains(t, body, "do active-task")
	require.NotContains(t, body, "do ready-task")

	frag := getPeek(t, mux, "/console/tasks/TR?reader=1")
	require.Equal(t, http.StatusOK, frag.Code)
	require.NotContains(t, frag.Body.String(), "<html")
	require.Contains(t, frag.Body.String(), "do ready-task")
}

func TestListQS(t *testing.T) {
	require.Equal(t, "", listQS("", "recent", "recent"))
	require.Equal(t, "?q=x", listQS("x", "recent", "recent"))
	require.Equal(t, "?sort=name", listQS("", "name", "recent"))
	require.Equal(t, "?q=a+b&sort=name", listQS("a b", "name", "recent"))
}
