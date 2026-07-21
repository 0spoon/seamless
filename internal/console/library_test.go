package console

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// The library screens (Memories / Notes / Tasks / Plans / Labs / Trials) render
// one unified page: orientation chrome, a grouped rail, and a reader. These
// tests cover the reader-specific behavior; the JSON list payloads and ?peek=1
// fragments are covered in the entity and peek tests.

func TestLibraryScreens_ShareOrientationChrome(t *testing.T) {
	_, mux := newConsole(t)
	for _, tc := range []struct {
		path string
		kind string
	}{
		{path: "/console/memories", kind: "memories"},
		{path: "/console/notes", kind: "notes"},
		{path: "/console/tasks", kind: "tasks"},
		{path: "/console/plans", kind: "plans"},
		{path: "/console/labs", kind: "labs"},
		{path: "/console/trials", kind: "trials"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			html := getPeek(t, mux, tc.path)
			require.Equal(t, http.StatusOK, html.Code)
			body := html.Body.String()
			require.Contains(t, body, `class="lib-page" data-library="`+tc.kind+`"`)
			require.Contains(t, body, `class="lib-hero"`)
			require.Contains(t, body, `class="lib-total"`)
		})
	}
}

func TestLibraryReaderStyles_FillAvailableColumn(t *testing.T) {
	css := string(consoleCSS)
	rule := func(selector string) string {
		t.Helper()
		start := strings.Index(css, selector+" {")
		require.NotEqual(t, -1, start, "missing %s rule", selector)
		end := strings.Index(css[start:], "}")
		require.NotEqual(t, -1, end, "unterminated %s rule", selector)
		return css[start : start+end]
	}

	// The shared reader contract keeps the navigation, sheet, and rendered
	// document aligned to the right edge of every library page. In particular,
	// none of these may regress to the legacy 900px / 72ch caps.
	for _, selector := range []string{".lib-reader", ".reader-nav", ".reader-sheet"} {
		got := rule(selector)
		require.Contains(t, got, `width: 100%`)
		require.NotContains(t, got, `max-width`)
	}
	doc := rule(".prose.doc")
	require.Contains(t, doc, `width: 100%`)
	require.Contains(t, doc, `max-width: none`)
}

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

func TestBuildNoteGroups_RecentOrdersNewestFirst(t *testing.T) {
	base := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	groups := buildNoteGroups(map[string][]noteRow{
		"seamless": {
			{ID: "OLD", Title: "Old", Updated: base},
			{ID: "NEW", Title: "New", Updated: base.Add(time.Hour)},
		},
	}, "recent")

	require.Len(t, groups, 1)
	require.Equal(t, []string{"NEW", "OLD"}, []string{groups[0].Notes[0].ID, groups[0].Notes[1].ID})
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
	require.Contains(t, body, `class="rail-subgroup-hd"`, "memory kind boundaries are explicit")
	require.Contains(t, body, `data-context="seamless / gotcha"`)
	require.Contains(t, body, `class="reader-sheet" data-memory-kind="gotcha"`)
	require.Contains(t, body, "Sort within kind", "the control names the sort boundary")

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
