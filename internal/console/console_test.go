package console

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/store"
)

const testKey = "test-key-abc123"

// newConsole builds a console over a fresh DB and returns the DB (for seeding)
// and its mux.
func newConsole(t *testing.T) (*sql.DB, *http.ServeMux) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc, err := New(Config{DB: db, Events: events.NewRecorder(db), APIKey: testKey})
	require.NoError(t, err)
	mux := http.NewServeMux()
	svc.Register(mux)
	return db, mux
}

// newTestMux builds a console over a fresh DB and returns its mux.
func newTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	_, mux := newConsole(t)
	return mux
}

// getJSON issues an authenticated JSON GET and decodes the body into v.
func getJSON(t *testing.T, mux *http.ServeMux, path string, v any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("Accept", "application/json")
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code, "GET %s -> %d: %s", path, rr.Code, rr.Body.String())
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), v))
}

func do(mux *http.ServeMux, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestAuth_UnauthenticatedRedirectsToLogin(t *testing.T) {
	mux := newTestMux(t)
	rr := do(mux, httptest.NewRequest(http.MethodGet, "/console/", nil))
	require.Equal(t, http.StatusSeeOther, rr.Code)
	require.Contains(t, rr.Header().Get("Location"), "/console/login")
}

func TestAuth_JSONUnauthenticatedReturns401(t *testing.T) {
	mux := newTestMux(t)
	rr := do(mux, httptest.NewRequest(http.MethodGet, "/console/?format=json", nil))
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	require.Contains(t, rr.Body.String(), "unauthorized")
}

func TestAuth_BearerKeyGrantsJSON(t *testing.T) {
	mux := newTestMux(t)
	req := httptest.NewRequest(http.MethodGet, "/console/?format=json", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Header().Get("Content-Type"), "application/json")
}

func TestLogin_WrongKeyRerendersWithError(t *testing.T) {
	mux := newTestMux(t)
	form := url.Values{"key": {"nope"}}
	req := httptest.NewRequest(http.MethodPost, "/console/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "invalid key")
	require.Empty(t, rr.Result().Cookies())
}

func TestLogin_CorrectKeySetsCookieAndGrantsAccess(t *testing.T) {
	mux := newTestMux(t)

	form := url.Values{"key": {testKey}, "next": {"/console/"}}
	req := httptest.NewRequest(http.MethodPost, "/console/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := do(mux, req)
	require.Equal(t, http.StatusSeeOther, rr.Code)

	var cookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == cookieName {
			cookie = c
		}
	}
	require.NotNil(t, cookie, "login must set the console cookie")
	require.Equal(t, consoleToken(testKey), cookie.Value)
	require.True(t, cookie.HttpOnly)

	// The cookie now authenticates a page load.
	req2 := httptest.NewRequest(http.MethodGet, "/console/", nil)
	req2.AddCookie(cookie)
	rr2 := do(mux, req2)
	require.Equal(t, http.StatusOK, rr2.Code)
	require.Contains(t, rr2.Body.String(), "Overview")
}

func TestLogout_ClearsCookie(t *testing.T) {
	mux := newTestMux(t)
	rr := do(mux, httptest.NewRequest(http.MethodPost, "/console/logout", nil))
	require.Equal(t, http.StatusSeeOther, rr.Code)
	var found bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == cookieName {
			found = true
			require.True(t, c.MaxAge < 0, "logout must expire the cookie")
		}
	}
	require.True(t, found)
}

func TestServeCSS(t *testing.T) {
	mux := newTestMux(t)
	rr := do(mux, httptest.NewRequest(http.MethodGet, "/console/static/console.css", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Header().Get("Content-Type"), "text/css")
	require.Contains(t, rr.Body.String(), ".sidebar")
}

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"/console/memories":   "/console/memories",
		"/console/":           "/console/",
		"https://evil.test/x": "/console/",
		"//evil.test":         "/console/",
		"/console//evil":      "/console/",
		"/etc/passwd":         "/console/",
		"":                    "/console/",
	}
	for in, want := range cases {
		require.Equal(t, want, safeNext(in), "safeNext(%q)", in)
	}
}

func TestGetNavCounts_EmptyDB(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	n, err := store.GetNavCounts(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, store.NavCounts{}, n)
}

func TestOverview_CoverageInPayload(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// One covered session (has findings), one uncovered (empty).
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: "cov1", Name: "cc/a", Status: core.SessionCompleted,
		Findings: "kept something", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: "cov2", Name: "cc/b", Status: core.SessionCompleted,
		CreatedAt: now, UpdatedAt: now,
	}))

	var data overviewData
	getJSON(t, mux, "/console/?format=json", &data)

	require.Equal(t, 1, data.Covered)
	require.Equal(t, 50, data.Coverage) // 1 of 2 sessions
	require.Len(t, data.CoverageRows, 4)
	require.Equal(t, "Findings", data.CoverageRows[0].Label)
	require.Equal(t, 1, data.CoverageRows[0].Count)
	require.Equal(t, 50, data.CoverageRows[0].Pct)

	// The trend spans the dense 14-day window and today reflects 1 of 2 covered.
	require.Len(t, data.CoverageTrend, 14)
	today := data.CoverageTrend[len(data.CoverageTrend)-1]
	require.Equal(t, now.Format("2006-01-02"), today.Day)
	require.Equal(t, 2, today.Total)
	require.Equal(t, 1, today.Covered)
}

func TestOverview_CoverageTrendEmptyWhenNoSessions(t *testing.T) {
	_, mux := newConsole(t)
	var data overviewData
	getJSON(t, mux, "/console/?format=json", &data)
	require.Nil(t, data.CoverageTrend)
}
