package config

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServerURL(t *testing.T) {
	tests := []struct{ name, addr, want string }{
		{"loopback default", "127.0.0.1:8081", "http://127.0.0.1:8081"},
		{"non-default port", "127.0.0.1:9099", "http://127.0.0.1:9099"},
		{"loopback name", "localhost:9000", "http://localhost:9000"},
		{"ipv6 loopback", "[::1]:8081", "http://[::1]:8081"},
		{"concrete lan host", "192.168.1.5:8081", "http://192.168.1.5:8081"},

		// A wildcard bind says where to listen, not where to dial.
		{"ipv4 wildcard", "0.0.0.0:8081", "http://127.0.0.1:8081"},
		{"bare port", ":8081", "http://127.0.0.1:8081"},
		{"ipv6 wildcard", "[::]:8081", "http://127.0.0.1:8081"},

		// Not host:port at all: nothing could have listened on it either.
		{"not host:port", "garbage", "http://127.0.0.1:8081"},
		{"empty", "", "http://127.0.0.1:8081"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Config{Addr: tt.addr}.ServerURL())
		})
	}
}

// The published cards and the docs site are generated from the defaults, so a
// drift here rewrites committed site output.
func TestServerURLOfDefaultsIsTheDocumentedEndpoint(t *testing.T) {
	require.Equal(t, "http://127.0.0.1:8081", Defaults().ServerURL())
	require.Equal(t, "http://"+Defaults().Addr, Defaults().ServerURL())
}

func TestServerHost(t *testing.T) {
	tests := []struct{ addr, want string }{
		{"127.0.0.1:8081", "127.0.0.1"},
		{"0.0.0.0:8081", "127.0.0.1"},
		{":8081", "127.0.0.1"},
		{"[::1]:8081", "::1"}, // brackets belong to the URL, not to the host
		{"[::]:8081", "127.0.0.1"},
		{"LOCALHOST:9000", "localhost"}, // Host headers do not preserve case
		{"192.168.1.5:8081", "192.168.1.5"},
		{"garbage", "127.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			require.Equal(t, tt.want, Config{Addr: tt.addr}.ServerHost())
		})
	}
}

// Both predicates are stubs until the transport keys land; pin them so the
// callers written against them today cannot be silently reinterpreted.
func TestRoleAndTLSPredicatesAreOffUntilTheirKeysLand(t *testing.T) {
	for _, c := range []Config{{}, Defaults(), {Addr: "0.0.0.0:8081"}} {
		require.False(t, c.IsClient())
		require.False(t, c.TLSEnabled())
	}
	require.True(t, strings.HasPrefix(Defaults().ServerURL(), "http://"),
		"the scheme follows TLSEnabled, which is off")
}

func TestHostname(t *testing.T) {
	got := Hostname()
	require.Equal(t, got, Hostname(), "cached: the second call returns the first value")
	require.Equal(t, strings.ToLower(got), got, "lower-cased")
	require.NotContains(t, got, " ", "trimmed")

	if h, err := os.Hostname(); err == nil {
		require.Equal(t, strings.ToLower(strings.TrimSpace(h)), got)
	}
}
