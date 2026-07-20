package console

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// consoleCookie returns a valid console session cookie for testKey.
func consoleCookie() *http.Cookie {
	return &http.Cookie{Name: cookieName, Value: consoleToken(testKey)}
}

func TestSecurityHeaders_OnEveryConsoleResponse(t *testing.T) {
	mux := newTestMux(t)

	// A public route, an authenticated route, and a redirect: the headers are
	// attached at registration, so none of the three can be missing them.
	for _, tc := range []struct {
		name string
		req  func() *http.Request
	}{
		{"public login page", func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "/console/login", nil)
		}},
		{"static asset", func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "/console/static/console.css", nil)
		}},
		{"unauthenticated redirect", func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "/console/", nil)
		}},
		{"authenticated page", func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/console/", nil)
			r.Header.Set("Authorization", "Bearer "+testKey)
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := do(mux, tc.req())
			require.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))
			require.Equal(t, "DENY", rr.Header().Get("X-Frame-Options"))
			require.Equal(t, "no-referrer", rr.Header().Get("Referrer-Policy"))
		})
	}
}

// The core M2 case: SameSite=Lax treats every 127.0.0.1:<port> as same-site, so
// a page on another local port can POST here with the console cookie attached.
// Sec-Fetch-Site is what distinguishes them.
func TestCookieWrite_RejectsCrossSitePOST(t *testing.T) {
	mux := newTestMux(t)

	req := httptest.NewRequest(http.MethodPost, "/console/settings/briefing/reset", nil)
	req.AddCookie(consoleCookie())
	req.Header.Set("Sec-Fetch-Site", "same-site") // another 127.0.0.1 port
	rr := do(mux, req)
	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Contains(t, rr.Body.String(), "cross-origin")
}

func TestCookieWrite_RejectsMismatchedOrigin(t *testing.T) {
	mux := newTestMux(t)

	// No Sec-Fetch-Site (older client): Origin is the fallback signal, and the
	// port must match -- this is the exact case SameSite=Lax lets through.
	req := httptest.NewRequest(http.MethodPost, "/console/settings/briefing/reset", nil)
	req.Host = "127.0.0.1:8081"
	req.AddCookie(consoleCookie())
	req.Header.Set("Origin", "http://127.0.0.1:3000")
	rr := do(mux, req)
	require.Equal(t, http.StatusForbidden, rr.Code)
}

func TestCookieWrite_RejectsWhenNoOriginSignal(t *testing.T) {
	mux := newTestMux(t)

	req := httptest.NewRequest(http.MethodPost, "/console/settings/briefing/reset", nil)
	req.AddCookie(consoleCookie())
	rr := do(mux, req)
	require.Equal(t, http.StatusForbidden, rr.Code,
		"a cookie write with no Sec-Fetch-Site and no Origin must not be trusted")
}

func TestCookieWrite_AllowsSameOrigin(t *testing.T) {
	mux := newTestMux(t)

	for _, tc := range []struct {
		name   string
		set    func(*http.Request)
		reason string
	}{
		{"sec-fetch-site same-origin", func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
		}, "the console's own forms"},
		{"sec-fetch-site none", func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "none")
		}, "a user-initiated navigation has no attacker initiator"},
		{"matching origin", func(r *http.Request) {
			r.Host = "127.0.0.1:8081"
			r.Header.Set("Origin", "http://127.0.0.1:8081")
		}, "same host AND port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/console/settings/briefing/reset", nil)
			req.AddCookie(consoleCookie())
			tc.set(req)
			rr := do(mux, req)
			require.NotEqual(t, http.StatusForbidden, rr.Code, tc.reason)
		})
	}
}

// The CLI presents the key in a header no browser attaches on its own, so it is
// not a CSRF vector and must not be forced to produce origin headers it has no
// reason to have.
func TestBearerWrite_SkipsOriginCheck(t *testing.T) {
	mux := newTestMux(t)

	req := httptest.NewRequest(http.MethodPost, "/console/settings/briefing/reset", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := do(mux, req)
	require.NotEqual(t, http.StatusForbidden, rr.Code)
}

// Reads are not writes: the origin check must not break ordinary navigation,
// including arriving from an external link.
func TestCookieRead_AllowedRegardlessOfOrigin(t *testing.T) {
	mux := newTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/console/", nil)
	req.AddCookie(consoleCookie())
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestSameOriginRequest_Table(t *testing.T) {
	for _, tc := range []struct {
		name    string
		host    string
		headers map[string]string
		want    bool
	}{
		{"sec-fetch-site wins over a forged Origin", "a:1",
			map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://a:1"}, false},
		{"origin scheme is ignored, host:port is not", "127.0.0.1:8081",
			map[string]string{"Origin": "https://127.0.0.1:8081"}, true},
		{"different port is cross-origin", "127.0.0.1:8081",
			map[string]string{"Origin": "http://127.0.0.1:8082"}, false},
		{"unparseable origin", "127.0.0.1:8081",
			map[string]string{"Origin": "::not a url::"}, false},
		{"null origin (sandboxed frame)", "127.0.0.1:8081",
			map[string]string{"Origin": "null"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/console/", nil)
			r.Host = tc.host
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			require.Equal(t, tc.want, sameOriginRequest(r))
		})
	}
}
