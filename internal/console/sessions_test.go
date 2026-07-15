package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/store"
)

func TestSessionsPage_ListAndDetail(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	rec := events.NewRecorder(db)

	id, err := core.NewID()
	require.NoError(t, err)
	now := time.Now().UTC()
	sess := core.Session{
		ID: id, Name: "cc/abcd1234", ProjectSlug: "seamless", Status: core.SessionCompleted,
		Findings: "found the bug in the watcher", Source: "startup", Ambient: true,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	require.NoError(t, store.CreateSession(ctx, db, sess))

	memID, _ := core.NewID()
	_, err = rec.Record(ctx, core.Event{Kind: core.EventToolCall, SessionID: id, Payload: map[string]any{"tool": "memory_write"}})
	require.NoError(t, err)
	_, err = rec.Record(ctx, core.Event{Kind: core.EventInjected, SessionID: id, Payload: map[string]any{"item_ids": []any{memID}}})
	require.NoError(t, err)
	_, err = rec.Record(ctx, core.Event{Kind: core.EventMemoryRead, SessionID: id, ItemID: memID, Payload: map[string]any{"name": "watcher-race"}})
	require.NoError(t, err)

	// List
	var list sessionsData
	getJSON(t, mux, "/console/sessions?format=json", &list)
	require.Equal(t, 1, list.Total)
	require.Equal(t, "all", list.Window, "defaults to the all-time window")
	require.Equal(t, 1, list.Completed)
	require.Zero(t, list.Active)
	require.Len(t, list.Sessions, 1)
	require.Equal(t, "cc/abcd1234", list.Sessions[0].Name)
	require.True(t, list.Sessions[0].Ambient)

	// Filter by active -> empty (our session is completed)
	var active sessionsData
	getJSON(t, mux, "/console/sessions?status=active&format=json", &active)
	require.Empty(t, active.Sessions)

	// Detail
	var detail sessionDetail
	getJSON(t, mux, "/console/sessions/"+id+"?format=json", &detail)
	require.Equal(t, id, detail.Session.ID)
	require.Equal(t, 1, detail.ToolCalls)
	require.Equal(t, 1, detail.Reads)
	require.Equal(t, 1, detail.Injected)
	require.Equal(t, 1, detail.ReadBack, "injected item was later read -> read-after-inject")
	require.Len(t, detail.Timeline, 3)

	// HTML list renders
	reqL := httptest.NewRequest(http.MethodGet, "/console/sessions", nil)
	reqL.Header.Set("Authorization", "Bearer "+testKey)
	rrL := do(mux, reqL)
	require.Equal(t, http.StatusOK, rrL.Code)
	require.Contains(t, rrL.Body.String(), "cc/abcd1234")

	// HTML detail renders
	req := httptest.NewRequest(http.MethodGet, "/console/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "found the bug in the watcher")
}

// TestSessionsPage_LiveIdleExpired covers the liveness split the reaper feeds:
// a heartbeated active session is live, an active session gone quiet past the idle
// TTL is idle (awaiting the reaper), and a reaped session is expired.
func TestSessionsPage_LiveIdleExpired(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mk := func(name string, updatedAgo time.Duration, status core.SessionStatus) {
		id, err := core.NewID()
		require.NoError(t, err)
		require.NoError(t, store.CreateSession(ctx, db, core.Session{
			ID: id, Name: name, ProjectSlug: "seamless", Status: status,
			CreatedAt: now.Add(-updatedAgo), UpdatedAt: now.Add(-updatedAgo),
		}))
	}
	mk("cc/live", 2*time.Minute, core.SessionActive)                   // heartbeated -> live
	mk("sess/idle", core.SessionIdleTTL+time.Hour, core.SessionActive) // quiet -> idle
	mk("sess/reaped", 3*time.Hour, core.SessionExpired)                // reaped -> expired

	var list sessionsData
	getJSON(t, mux, "/console/sessions?format=json", &list)
	require.Equal(t, 1, list.Active, "only the heartbeated session is live")
	require.Equal(t, 1, list.Idle, "the quiet active session is idle")
	require.Equal(t, 1, list.Expired)

	byName := map[string]sessionRow{}
	for _, r := range list.Sessions {
		byName[r.Name] = r
	}
	require.True(t, byName["cc/live"].Live)
	require.False(t, byName["sess/idle"].Live, "idle session is not live")
	require.Equal(t, "expired", byName["sess/reaped"].Status)

	// The expired filter lists only the reaped session.
	var expired sessionsData
	getJSON(t, mux, "/console/sessions?status=expired&format=json", &expired)
	require.Len(t, expired.Sessions, 1)
	require.Equal(t, "sess/reaped", expired.Sessions[0].Name)
}

// `?status=bogus` used to hit `default: filter = ""` and list EVERY session --
// indistinguishable, to the caller, from a filter that matched everything. It is
// the worst case of seam's `--status` bug: the client-side enum stops a typo from
// a seam user, but nothing stopped a direct URL or another client.
//
// The inconsistency this closes lived inside one handler: a bad ?sort has always
// been a loud 400.
func TestSessionsPage_BadStatusIsRejected(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	id, err := core.NewID()
	require.NoError(t, err)
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: id, Name: "cc/one", Status: core.SessionActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))

	req := httptest.NewRequest(http.MethodGet, "/console/sessions?status=bogus&format=json", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("Accept", "application/json")
	rr := do(mux, req)
	require.Equal(t, http.StatusBadRequest, rr.Code,
		"a session that exists must not come back as a plausible answer to a bogus filter")
	// Decoded, not the raw body: the message quotes the offending value, and JSON
	// escapes those quotes.
	var e struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &e))
	// Derived from core.SessionStatuses, so it names every real value -- including
	// "expired", which seam's old --status help text omitted.
	require.Equal(t, `invalid status "bogus": valid values are active, completed, expired`, e.Error)

	// An ABSENT status stays a legitimate default: no filter, list everything.
	var all sessionsData
	getJSON(t, mux, "/console/sessions?format=json", &all)
	require.Len(t, all.Sessions, 1)
}

func TestSessionsPage_WindowFilter(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mk := func(id string, updated time.Time) core.Session {
		return core.Session{
			ID: id, Name: "cc/" + id, Status: core.SessionCompleted,
			CreatedAt: updated, UpdatedAt: updated,
		}
	}
	require.NoError(t, store.CreateSession(ctx, db, mk("recent", now.Add(-time.Hour))))
	require.NoError(t, store.CreateSession(ctx, db, mk("old", now.Add(-72*time.Hour))))

	// All-time lists both; the 24h window drops the 72h-old session.
	var all sessionsData
	getJSON(t, mux, "/console/sessions?format=json", &all)
	require.Len(t, all.Sessions, 2)

	var day sessionsData
	getJSON(t, mux, "/console/sessions?w=24h&format=json", &day)
	require.Len(t, day.Sessions, 1)
	require.Equal(t, "cc/recent", day.Sessions[0].Name)
	require.Equal(t, "24h", day.Window)
	require.Equal(t, 2, day.Total, "Total is the all-time count, not the windowed one")
}

func TestSessionDetail_NotFound(t *testing.T) {
	_, mux := newConsole(t)
	req := httptest.NewRequest(http.MethodGet, "/console/sessions/NOSUCHID", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

// TestSessionDetail_ClaimedTaskAndMemories covers the T2b relation joins the
// session-detail rail renders: the live task a session holds and the memories it
// produced.
func TestSessionDetail_ClaimedTaskAndMemories(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.CreateProject(ctx, db, core.Project{
		ID: mustID(t), Slug: "seamless", Name: "Seamless", CreatedAt: now, UpdatedAt: now,
	}))
	sessID := mustID(t)
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: sessID, Name: "cc/holder", ProjectSlug: "seamless", Status: core.SessionActive,
		CreatedAt: now, UpdatedAt: now,
	}))
	taskID := mustID(t)
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: taskID, ProjectSlug: "seamless", Title: "Wire the rail", Status: core.TaskOpen,
		PlanSlug: "detail", CreatedAt: now, UpdatedAt: now,
	}))
	_, err := store.ClaimTask(ctx, db, taskID, sessID, 30*time.Minute, now)
	require.NoError(t, err)
	insertRawMemory(t, db, mustID(t), "seamless", "cc/holder", now)

	var detail sessionDetail
	getJSON(t, mux, "/console/sessions/"+sessID+"?format=json", &detail)
	require.Len(t, detail.ClaimedTasks, 1, "the live claim shows on the rail")
	require.Equal(t, "Wire the rail", detail.ClaimedTasks[0].Title)
	require.Equal(t, "detail", detail.ClaimedTasks[0].PlanSlug)
	require.NotEmpty(t, detail.ClaimedTasks[0].LeaseLeft)
	require.Len(t, detail.Memories, 1, "the produced memory shows on the rail")

	// The HTML page renders both panels.
	body := getHTMLBody(t, mux, "/console/sessions/"+sessID)
	require.Contains(t, body, "Claimed task")
	require.Contains(t, body, "Wire the rail")
	require.Contains(t, body, "Memories written")
}

// TestOverview_ProjectsAtAGlance covers the projects-at-a-glance table: strict
// per-slug rows sorted by recent activity, from the batched board query.
func TestOverview_ProjectsAtAGlance(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.CreateProject(ctx, db, core.Project{
		ID: mustID(t), Slug: "older", Name: "older", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, store.CreateProject(ctx, db, core.Project{
		ID: mustID(t), Slug: "newer", Name: "newer", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: mustID(t), Name: "cc/o", ProjectSlug: "older", Status: core.SessionCompleted,
		CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-48 * time.Hour),
	}))
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: mustID(t), Name: "cc/n", ProjectSlug: "newer", Status: core.SessionActive,
		CreatedAt: now, UpdatedAt: now,
	}))
	// Active but idle (last updated beyond SessionIdleTTL, not yet reaped): still
	// a session, never live -- on the glance rows or the headline count.
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: mustID(t), Name: "cc/i", ProjectSlug: "older", Status: core.SessionActive,
		CreatedAt: now.Add(-2 * core.SessionIdleTTL), UpdatedAt: now.Add(-2 * core.SessionIdleTTL),
	}))

	var data overviewData
	getJSON(t, mux, "/console/?format=json", &data)
	require.Len(t, data.Projects, 2)
	require.Equal(t, "newer", data.Projects[0].Slug, "most recently active project first")
	require.Equal(t, "older", data.Projects[1].Slug)
	require.Equal(t, 1, data.Projects[0].Live, "the active session counts as live")
	require.Equal(t, 0, data.Projects[1].Live, "an idle active session is not live")
	require.Equal(t, 1, data.SessActive, "the headline counts live sessions, not raw active status")
	require.Equal(t, 3, data.SessTotal)
}
