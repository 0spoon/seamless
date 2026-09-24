package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot) // distinctive: proves we reached through
	})
}

func TestHostGuard_LoopbackBind(t *testing.T) {
	h := hostGuard("127.0.0.1:8081", nil, okHandler())

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
	h := hostGuard("192.168.1.5:8081", nil, okHandler())

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
		h := hostGuard(bind, nil, okHandler())
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Host = "anything.example.com"
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, http.StatusTeapot, rr.Code, "bind %s", bind)
	}
}

// Extra allowlist entries are additive: they join the loopback names and the
// bind host without displacing either, and they are matched case-insensitively
// like every other Host comparison.
func TestHostGuard_ExtraHostsJoinTheAllowlist(t *testing.T) {
	h := hostGuard("192.168.1.5:8081", []string{"Seam.lan", " ", ""}, okHandler())

	for host, want := range map[string]int{
		"seam.lan:8081":         http.StatusTeapot,
		"SEAM.LAN":              http.StatusTeapot,
		"192.168.1.5:8081":      http.StatusTeapot, // the bind host is still allowed
		"127.0.0.1:8081":        http.StatusTeapot, // and so is loopback
		"evil.example.com:8081": http.StatusMisdirectedRequest,
	} {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Host = host
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, want, rr.Code, "Host: %s", host)
	}
}

// Naming a host is what makes a wildcard bind guardable: with an allowlist the
// guard is on even for 0.0.0.0, and the wildcard itself never joins the list
// (an empty Host header must not match a bare ":8081" bind).
func TestHostGuard_WildcardBindWithExtraHostsIsGuarded(t *testing.T) {
	for _, bind := range []string{"0.0.0.0:8081", ":8081", "[::]:8081"} {
		h := hostGuard(bind, []string{"seam.lan"}, okHandler())
		for host, want := range map[string]int{
			"seam.lan:8081":    http.StatusTeapot,
			"127.0.0.1:8081":   http.StatusTeapot,
			"anything.example": http.StatusMisdirectedRequest,
			"":                 http.StatusMisdirectedRequest,
		} {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Host = host
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			require.Equal(t, want, rr.Code, "bind %s, Host: %s", bind, host)
		}
	}
}

// An allowlist of nothing but blanks is not an allowlist: a wildcard bind stays
// unguarded rather than admitting only loopback and locking the operator out.
func TestHostGuard_BlankExtraHostsStayUnguardedOnAWildcardBind(t *testing.T) {
	h := hostGuard("0.0.0.0:8081", []string{"", "   "}, okHandler())
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "anything.example.com"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusTeapot, rr.Code)
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

// The wiring runServe performs: the allowlist is config.AllowedHostsEffective,
// so naming the daemon in server_url is what both advertises it and admits it.
// Without this the operator sets server_url, every remote client dials the name
// it publishes, and the guard answers 421 to all of them.
func TestHostGuard_AllowlistFromServerURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.Config
		want map[string]int
	}{
		{
			name: "wildcard bind, server_url names the daemon",
			cfg:  config.Config{Addr: "0.0.0.0:8081", AdvertisedURL: "http://seam.lan:8081"},
			want: map[string]int{
				"seam.lan:8081":  http.StatusTeapot,
				"seam.lan":       http.StatusTeapot,
				"127.0.0.1:8081": http.StatusTeapot,
				"evil.example":   http.StatusMisdirectedRequest,
			},
		},
		{
			name: "concrete bind, server_url plus extra hosts",
			cfg: config.Config{
				Addr: "192.168.1.5:8081", AdvertisedURL: "https://seam.lan",
				AllowedHosts: []string{"seam", "mac.local"},
			},
			want: map[string]int{
				"seam.lan":         http.StatusTeapot,
				"seam":             http.StatusTeapot,
				"mac.local:8081":   http.StatusTeapot,
				"192.168.1.5:8081": http.StatusTeapot, // the bind host, unconditionally
				"evil.example":     http.StatusMisdirectedRequest,
			},
		},
		{
			name: "wildcard bind with nothing named stays unguarded",
			cfg:  config.Config{Addr: "0.0.0.0:8081"},
			want: map[string]int{
				// ServerHost of a wildcard bind is loopback, which is already in
				// the list -- so it is not a name the operator supplied, and the
				// guard must not switch on because of it.
				"anything.example": http.StatusTeapot,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := hostGuard(tc.cfg.Addr, tc.cfg.AllowedHostsEffective(), okHandler())
			for host, want := range tc.want {
				req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
				req.Host = host
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, req)
				require.Equal(t, want, rr.Code, "Host: %s", host)
			}
		})
	}
}

// warnLines captures the slog output of fn, one entry per line.
func warnLines(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

// The protection line is a claim about this listener, so it must track TLS. A
// warning that says "no TLS on this listener" to an operator who just configured
// TLS is how a security warning trains its reader to ignore it.
func TestWarnNonLoopbackBind(t *testing.T) {
	require.Empty(t, warnLines(t, func() { warnNonLoopbackBind("127.0.0.1:8081", false) }),
		"loopback is the designed model; no warning")

	plain := warnLines(t, func() { warnNonLoopbackBind("0.0.0.0:8081", false) })
	require.Contains(t, plain, "SECURITY: binding to a non-loopback address")
	require.Contains(t, plain, "the console cookie is not Secure")
	require.Contains(t, plain, "set tls.cert_file/tls.key_file or keep it inside a trusted LAN")

	secure := warnLines(t, func() { warnNonLoopbackBind("0.0.0.0:8081", true) })
	require.Contains(t, secure, "SECURITY: binding to a non-loopback address")
	require.Contains(t, secure, "the console cookie is Secure")
	require.NotContains(t, secure, "no TLS on this listener")
	require.NotContains(t, secure, "sent in the clear")
}

func TestWarnAdvertisedLoopback(t *testing.T) {
	for _, tc := range []struct {
		name, bind, url string
		wantWarn        bool
	}{
		{"loopback bind is the normal install", "127.0.0.1:8081", "http://127.0.0.1:8081", false},
		{"wide bind, loopback url", "0.0.0.0:8081", "http://127.0.0.1:8081", true},
		{"wide bind, localhost url", "0.0.0.0:8081", "http://localhost:8081", true},
		{"wide bind, ipv6 loopback url", "0.0.0.0:8081", "http://[::1]:8081", true},
		{"wide bind, 127/8 url", "0.0.0.0:8081", "http://127.0.0.2:8081", true},
		{"wide bind, named url", "0.0.0.0:8081", "http://seam.lan:8081", false},
		{"concrete lan bind, own address", "192.168.1.5:8081", "http://192.168.1.5:8081", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := warnLines(t, func() { warnAdvertisedLoopback(tc.bind, tc.url) })
			if tc.wantWarn {
				require.Contains(t, out, "server_url still points at this machine")
				return
			}
			require.Empty(t, out)
		})
	}
}
