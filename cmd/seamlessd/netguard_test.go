package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot) // distinctive: proves we reached through
	})
}

func TestHostGuard_LoopbackBind(t *testing.T) {
	h := hostGuard("127.0.0.1:8081", okHandler())

	for _, tc := range []struct {
		name string
		host string
		want int
	}{
		{"ipv4 loopback with port", "127.0.0.1:8081", http.StatusTeapot},
		{"ipv4 loopback bare", "127.0.0.1", http.StatusTeapot},
		{"localhost", "localhost:8081", http.StatusTeapot},
		{"ipv6 loopback bracketed", "[::1]:8081", http.StatusTeapot},
		{"case-insensitive", "LOCALHOST:8081", http.StatusTeapot},

		// The DNS-rebinding case: the attacker controls the name and can point
		// it at 127.0.0.1, but not the Host header the browser then sends.
		{"rebound attacker domain", "evil.example.com:8081", http.StatusMisdirectedRequest},
		{"rebound domain, no port", "evil.example.com", http.StatusMisdirectedRequest},
		{"lan address", "192.168.1.5:8081", http.StatusMisdirectedRequest},
		{"empty host", "", http.StatusMisdirectedRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Host = tc.host
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			require.Equal(t, tc.want, rr.Code)
		})
	}
}

// A concrete non-loopback bind is a deliberate choice, so that address joins the
// allowlist -- but a rebound name still does not.
func TestHostGuard_ConcreteNonLoopbackBindIsAllowlisted(t *testing.T) {
	h := hostGuard("192.168.1.5:8081", okHandler())

	for host, want := range map[string]int{
		"192.168.1.5:8081":      http.StatusTeapot,
		"127.0.0.1:8081":        http.StatusTeapot,
		"evil.example.com:8081": http.StatusMisdirectedRequest,
	} {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Host = host
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, want, rr.Code, "Host: %s", host)
	}
}

// A wildcard bind has no knowable set of valid Host values, so the guard steps
// aside rather than guessing and breaking the operator's setup.
func TestHostGuard_WildcardBindPassesEverythingThrough(t *testing.T) {
	for _, bind := range []string{"0.0.0.0:8081", ":8081", "[::]:8081"} {
		h := hostGuard(bind, okHandler())
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Host = "anything.example.com"
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, http.StatusTeapot, rr.Code, "bind %s", bind)
	}
}

func TestIsLoopbackBind(t *testing.T) {
	for bind, want := range map[string]bool{
		"127.0.0.1:8081": true,
		"localhost:8081": true,
		"[::1]:8081":     true,
		"127.0.0.2:8081": true, // the whole 127/8 block is loopback
		"0.0.0.0:8081":   false,
		":8081":          false,
		"[::]:8081":      false,
		"192.168.1.5:80": false,
		"example.com:80": false, // unresolvable name: warn rather than stay quiet
	} {
		require.Equal(t, want, isLoopbackBind(bind), "bind %s", bind)
	}
}
