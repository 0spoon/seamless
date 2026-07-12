package console

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/store"
)

// getPeek issues an authenticated GET and returns the recorder (for asserting on
// the raw body / status).
func getPeek(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	return do(mux, req)
}

func writeNote(t *testing.T, mgr *files.Manager, project, slug, title, body string) core.Note {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	now := time.Now().UTC()
	n, err := mgr.WriteNote(context.Background(), core.Note{
		ID: id, Title: title, Slug: slug, Project: project, Body: body,
		Created: now, Updated: now,
	})
	require.NoError(t, err)
	return n
}

// insertMemoryRow inserts a bare memories_index row (no file), for the nil-Files
// path where there is no source file to read a body from.
func insertMemoryRow(t *testing.T, db *sql.DB, id, name, project string) {
	t.Helper()
	now := core.FormatTime(time.Now().UTC())
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO memories_index
		    (id, kind, name, description, project, file_path, tags, valid_from,
		     invalid_at, superseded_by, source_session, content_hash, created_at, updated_at)
		VALUES (?, 'gotcha', ?, 'desc', ?, ?, '[]', ?, NULL, NULL, '', 'h', ?, ?)`,
		id, name, project, "memory/"+name+".md", now, now, now)
	require.NoError(t, err)
}

func TestMemoryPeek_FragmentFullJSON(t *testing.T) {
	db, mgr, mux := newConsoleWithFiles(t)
	_ = db

	target := writeMemory(t, mgr, core.KindGotcha, "seamless", "target-mem", "the target")
	src := writeMemory(t, mgr, core.KindConstraint, "seamless", "src-mem", "the source")
	// Give src a body that links to target.
	src.Body = "prose before [[target-mem]] and after"
	_, err := mgr.WriteMemory(context.Background(), src)
	require.NoError(t, err)

	// Fragment: no layout wrapper, but the linkified body + peek link are present.
	frag := getPeek(t, mux, "/console/memories/"+src.ID+"?peek=1")
	require.Equal(t, http.StatusOK, frag.Code)
	body := frag.Body.String()
	require.NotContains(t, body, "<html", "fragment must not carry the page layout")
	require.Contains(t, body, "src-mem")
	require.Contains(t, body, "/console/memories/"+target.ID)
	require.Contains(t, body, "data-peek")

	// Full page: layout wrapper present.
	full := getPeek(t, mux, "/console/memories/"+src.ID)
	require.Equal(t, http.StatusOK, full.Code)
	require.Contains(t, full.Body.String(), "<html")
	require.Contains(t, full.Body.String(), "src-mem")

	// JSON: raw payload.
	var d memoryDetail
	getJSON(t, mux, "/console/memories/"+src.ID+"?format=json", &d)
	require.Equal(t, "src-mem", d.Name)
	require.True(t, d.BodyLoaded)
	require.Contains(t, d.BodyText, "[[target-mem]]")
	require.Equal(t, "active", d.Status)

	// Missing id -> 404.
	require.Equal(t, http.StatusNotFound, getPeek(t, mux, "/console/memories/nope").Code)
}

func TestMemoryPeek_SupersessionAndStats(t *testing.T) {
	db, mgr, mux := newConsoleWithFiles(t)
	ctx := context.Background()

	old := writeMemory(t, mgr, core.KindGotcha, "seamless", "old-mem", "old")
	newer := writeMemory(t, mgr, core.KindGotcha, "seamless", "new-mem", "new")
	// old is superseded by newer.
	_, err := db.ExecContext(ctx, `UPDATE memories_index SET superseded_by=?, invalid_at=? WHERE id=?`,
		newer.ID, core.FormatTime(time.Now().UTC()), old.ID)
	require.NoError(t, err)

	// Record an injection + a read of newer, then materialize the stats.
	rec := events.NewRecorder(db)
	_, err = rec.Record(ctx, core.Event{Kind: core.EventInjected, ItemID: newer.ID})
	require.NoError(t, err)
	_, err = rec.Record(ctx, core.Event{Kind: core.EventMemoryRead, ItemID: newer.ID})
	require.NoError(t, err)
	require.NoError(t, store.RebuildRetrievalStats(ctx, db))

	// newer: reverse "supersedes" edge + stats.
	var nd memoryDetail
	getJSON(t, mux, "/console/memories/"+newer.ID+"?format=json", &nd)
	require.Len(t, nd.Supersedes, 1)
	require.Equal(t, old.ID, nd.Supersedes[0].ID)
	require.Equal(t, 1, nd.Injects)
	require.Equal(t, 1, nd.Reads)

	// old: forward "superseded by" edge + status.
	var od memoryDetail
	getJSON(t, mux, "/console/memories/"+old.ID+"?format=json", &od)
	require.Equal(t, "superseded", od.Status)
	require.Equal(t, newer.ID, od.ReplacedByID)
	require.Equal(t, "new-mem", od.ReplacedBy)
}

func TestMemoryPeek_NilFilesMetadataOnly(t *testing.T) {
	db, mux := newConsole(t) // no Files subsystem
	insertMemoryRow(t, db, "01MEM", "no-body", "seamless")

	var d memoryDetail
	getJSON(t, mux, "/console/memories/01MEM?format=json", &d)
	require.Equal(t, "no-body", d.Name)
	require.False(t, d.BodyLoaded, "body is unavailable without a Files subsystem")
	require.False(t, d.CanArchive)

	// The fragment still renders (metadata only).
	frag := getPeek(t, mux, "/console/memories/01MEM?peek=1")
	require.Equal(t, http.StatusOK, frag.Code)
	require.Contains(t, frag.Body.String(), "Body unavailable")
}

func TestNotePeek(t *testing.T) {
	_, mgr, mux := newConsoleWithFiles(t)
	n := writeNote(t, mgr, "seamless", "research-note", "Research Note", "body text here")

	var d noteDetail
	getJSON(t, mux, "/console/notes/"+n.ID+"?format=json", &d)
	require.Equal(t, "Research Note", d.Title)
	require.True(t, d.BodyLoaded)
	require.Contains(t, d.BodyText, "body text here")

	frag := getPeek(t, mux, "/console/notes/"+n.ID+"?peek=1")
	require.Equal(t, http.StatusOK, frag.Code)
	require.NotContains(t, frag.Body.String(), "<html")
	require.Contains(t, frag.Body.String(), "Research Note")

	require.Equal(t, http.StatusNotFound, getPeek(t, mux, "/console/notes/nope").Code)
}

func TestTaskPeek_DepsAndBlocks(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	mk := func(id, title string, deps ...string) {
		require.NoError(t, store.CreateTask(ctx, db, core.Task{
			ID: id, ProjectSlug: "seamless", Title: title, Body: "do " + title,
			Status: core.TaskOpen, DependsOn: deps, CreatedAt: base, UpdatedAt: base,
		}))
	}
	mk("TA", "A")
	mk("TB", "B", "TA")
	mk("TC", "C", "TA")

	// A: rendered body + reverse "blocks" (B and C).
	var a taskDetail
	getJSON(t, mux, "/console/tasks/TA?format=json", &a)
	require.Equal(t, "do A", a.Body)
	var blocks []string
	for _, r := range a.Blocks {
		blocks = append(blocks, r.ID)
	}
	require.ElementsMatch(t, []string{"TB", "TC"}, blocks)
	require.Empty(t, a.Deps)

	// B: forward "depends on" (A).
	var b taskDetail
	getJSON(t, mux, "/console/tasks/TB?format=json", &b)
	require.Len(t, b.Deps, 1)
	require.Equal(t, "TA", b.Deps[0].ID)

	// Fragment renders the body; missing id 404s.
	frag := getPeek(t, mux, "/console/tasks/TA?peek=1")
	require.Equal(t, http.StatusOK, frag.Code)
	require.Contains(t, frag.Body.String(), "do A")
	require.Equal(t, http.StatusNotFound, getPeek(t, mux, "/console/tasks/nope").Code)
}

func TestProjectPeek_Counts(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	_, err := store.EnsureProject(ctx, db, "seamless", "Seamless")
	require.NoError(t, err)
	insertMemoryRow(t, db, "01PM", "pm", "seamless")
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: "01PT", ProjectSlug: "seamless", Title: "t", Status: core.TaskOpen,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))

	var d projectDetail
	getJSON(t, mux, "/console/projects/seamless?format=json", &d)
	require.Equal(t, "seamless", d.Slug)
	require.Equal(t, 1, d.Memories)
	require.Equal(t, 1, d.OpenTasks)

	require.Equal(t, http.StatusNotFound, getPeek(t, mux, "/console/projects/nope").Code)
}

func TestNotesPage(t *testing.T) {
	_, mgr, mux := newConsoleWithFiles(t)
	writeNote(t, mgr, "seamless", "n-a", "Alpha Note", "a")
	writeNote(t, mgr, "", "n-b", "Inbox Note", "b")

	var data notesData
	getJSON(t, mux, "/console/notes?format=json", &data)
	require.Equal(t, 2, data.Count)
	// Inbox ("") group sorts first.
	require.Equal(t, "", data.Groups[0].Project)
	require.Equal(t, "seamless", data.Groups[1].Project)

	html := getPeek(t, mux, "/console/notes")
	require.Equal(t, http.StatusOK, html.Code)
	require.Contains(t, html.Body.String(), "Alpha Note")
}

func TestSessionAndEventPeekFragments(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: "SESS1", Name: "cc/peek1234", ProjectSlug: "seamless", Status: core.SessionCompleted,
		Findings: "the finding text", Source: "startup", Ambient: true,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}))
	evID, err := events.NewRecorder(db).Record(ctx, core.Event{
		Kind: core.EventInjected, SessionID: "SESS1", ProjectSlug: "seamless",
		Payload: map[string]any{"hook": "SessionStart", "content": "briefing text"},
	})
	require.NoError(t, err)

	// Session peek: fragment (no layout), full page (layout), with content.
	sp := getPeek(t, mux, "/console/sessions/SESS1?peek=1")
	require.Equal(t, http.StatusOK, sp.Code)
	require.NotContains(t, sp.Body.String(), "<html")
	require.Contains(t, sp.Body.String(), "cc/peek1234")
	require.Contains(t, sp.Body.String(), "the finding text")
	require.Contains(t, getPeek(t, mux, "/console/sessions/SESS1").Body.String(), "<html")

	// Event peek: fragment shows the injected content + session peek link.
	ep := getPeek(t, mux, "/console/events/"+evID+"?peek=1")
	require.Equal(t, http.StatusOK, ep.Code)
	require.NotContains(t, ep.Body.String(), "<html")
	require.Contains(t, ep.Body.String(), "briefing text")
	require.Contains(t, ep.Body.String(), "/console/sessions/SESS1")
}

func TestLinkifyBody(t *testing.T) {
	resolve := func(name string) (string, bool) {
		if name == "known" {
			return "ID123", true
		}
		return "", false
	}
	cases := []struct {
		name        string
		body        string
		contains    []string
		notContains []string
	}{
		{
			name:     "resolved link",
			body:     "see [[known]] here",
			contains: []string{`<a href="/console/memories/ID123" data-peek>known</a>`, "see ", " here"},
		},
		{
			name:        "unresolved stays plain",
			body:        "see [[missing]] here",
			contains:    []string{"[[missing]]"},
			notContains: []string{"<a "},
		},
		{
			name:        "escapes html in body",
			body:        "<script>evil()</script> [[known]]",
			contains:    []string{"&lt;script&gt;", `<a href="/console/memories/ID123" data-peek>known</a>`},
			notContains: []string{"<script>"},
		},
		{
			name:        "no double escaping",
			body:        "a & b [[known]]",
			contains:    []string{"a &amp; b "},
			notContains: []string{"&amp;amp;"},
		},
		{
			name:     "project-qualified resolves via bare name",
			body:     "ref [[proj/known|Alias]]",
			contains: []string{`<a href="/console/memories/ID123" data-peek>known</a>`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(linkifyBody(tc.body, resolve))
			for _, c := range tc.contains {
				require.Contains(t, got, c)
			}
			for _, c := range tc.notContains {
				require.NotContains(t, got, c)
			}
		})
	}
}
