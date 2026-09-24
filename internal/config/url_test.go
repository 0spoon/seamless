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

// The default install is a plaintext loopback server: both predicates off, and
// the scheme http. A change here changes what every client dials.
func TestRoleAndTLSPredicatesAreOffByDefault(t *testing.T) {
	for _, c := range []Config{{}, Defaults(), {Addr: "0.0.0.0:8081"}} {
		require.False(t, c.IsClient())
		require.False(t, c.TLSEnabled())
	}
	require.True(t, strings.HasPrefix(Defaults().ServerURL(), "http://"),
		"the scheme follows TLSEnabled, which is off")
}

func TestIsClient(t *testing.T) {
	require.True(t, Config{Role: RoleClient}.IsClient())
	require.False(t, Config{Role: RoleServer}.IsClient())
	require.False(t, Config{Role: ""}.IsClient(), "absent means the default, which is server")
}

func TestTLSEnabledNeedsBothHalves(t *testing.T) {
	require.True(t, Config{TLS: TLS{CertFile: "c.pem", KeyFile: "k.pem"}}.TLSEnabled())
	require.False(t, Config{TLS: TLS{CertFile: "c.pem"}}.TLSEnabled())
	require.False(t, Config{TLS: TLS{KeyFile: "k.pem"}}.TLSEnabled())
	require.False(t, Config{TLS: TLS{CAFile: "ca.pem"}}.TLSEnabled(),
		"ca_file is the client's trust root, not a server certificate")
}

// A TLS daemon must advertise https, or every client it hands the URL to dials
// a port that will not speak plaintext back.
func TestServerURLSchemeFollowsTLS(t *testing.T) {
	c := Config{Addr: "0.0.0.0:8081", TLS: TLS{CertFile: "c.pem", KeyFile: "k.pem"}}
	require.Equal(t, "https://127.0.0.1:8081", c.ServerURL())
	require.Equal(t, "127.0.0.1", c.ServerHost())
}

// server_url wins over every derivation: it is the only key that can say where
// clients reach a daemon whose bind address is not that answer.
func TestServerURLAdvertisedWins(t *testing.T) {
	tests := []struct{ name, addr, advertised, want string }{
		{"lan name over wildcard bind", "0.0.0.0:8081", "http://seam.lan:8081", "http://seam.lan:8081"},
		{"https over a plaintext bind", "127.0.0.1:8081", "https://seam.lan", "https://seam.lan"},
		{"trailing slash trimmed", "127.0.0.1:8081", "http://seam.lan:8081/", "http://seam.lan:8081"},
		{"surrounding space trimmed", "127.0.0.1:8081", "  http://seam.lan:8081  ", "http://seam.lan:8081"},
		{"empty falls back to addr", "192.168.1.5:8081", "", "http://192.168.1.5:8081"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Config{Addr: tt.addr, AdvertisedURL: tt.advertised}.ServerURL())
		})
	}
	require.Equal(t, "seam.lan",
		Config{Addr: "0.0.0.0:8081", AdvertisedURL: "http://SEAM.LAN:8081"}.ServerHost(),
		"a Host header does not preserve case")
}

// A wildcard is the one host server_url must not carry: ServerURL returns a
// configured value verbatim, so an accepted wildcard reaches every client
// surface intact -- including the pairing one-liner `seamlessd client-config`
// prints for an operator to paste on ANOTHER machine, where 0.0.0.0 dials
// nothing. The bind address that made the wildcard tempting is unaffected.
func TestValidateServerURLRefusesWildcardHost(t *testing.T) {
	refused := []struct{ raw, host string }{
		{"http://0.0.0.0:8081", "0.0.0.0"},
		{"https://0.0.0.0:8081", "0.0.0.0"},
		{"http://0.0.0.0", "0.0.0.0"},
		{"http://[::]:8081", "::"}, // url.Hostname strips the brackets
		{"http://[::]", "::"},
		{"  http://0.0.0.0:8081/  ", "0.0.0.0"}, // trimmed first, still refused
	}
	for _, tt := range refused {
		t.Run(tt.raw, func(t *testing.T) {
			err := validateServerURL(tt.raw)
			require.Error(t, err)
			require.Contains(t, err.Error(), "wildcard host")
			require.Contains(t, err.Error(), tt.host, "the error names the offending host")
		})
	}

	accepted := []string{
		"",                        // unset: the derivation takes over
		"http://[fd00::1]:8081",   // a real IPv6 literal is not a wildcard
		"http://[::1]:8081",       // nor is IPv6 loopback
		"http://127.0.0.1:8081",   // loopback is legal here; client-config is what refuses it
		"http://localhost:8081",   // the same, by name
		"http://192.168.1.5:8081", // the LAN address this rule tells operators to name
		"https://seam.lan",
	}
	for _, raw := range accepted {
		t.Run("accepted "+raw, func(t *testing.T) {
			require.NoError(t, validateServerURL(raw))
		})
	}

	require.Contains(t, validateServerURL("http://").Error(), "names no host",
		"an empty host is a wildcard to reachableHost, but the earlier and more specific error wins")
}

// The refusal has to fire on the config-load path, not just in the private
// helper: Validate is what config.Load, EnsureClientConfig and every consumer
// of a loaded Config stand behind.
func TestValidateRefusesWildcardServerURL(t *testing.T) {
	c := Defaults()
	c.Addr = "0.0.0.0:8081"
	require.NoError(t, c.Validate(), "a wildcard BIND is a legitimate answer and stays one")

	c.AdvertisedURL = "http://0.0.0.0:8081"
	err := c.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "server_url")
	require.Contains(t, err.Error(), "0.0.0.0")

	c.AdvertisedURL = "http://seam.lan:8081"
	require.NoError(t, c.Validate(), "naming a reachable host is the fix the error asks for")
}

func TestAllowedHostsEffective(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{
			// A derived host is the bind host or loopback, both of which
			// hostGuard adds itself -- and a non-empty list here is what arms
			// the guard on a wildcard bind.
			name: "nothing named, nothing added",
			cfg:  Config{Addr: "127.0.0.1:8081"},
			want: nil,
		},
		{
			name: "wildcard bind, nothing named",
			cfg:  Config{Addr: "0.0.0.0:8081"},
			want: nil,
		},
		{
			name: "a configured server_url is always in the list",
			cfg:  Config{Addr: "0.0.0.0:8081", AdvertisedURL: "http://seam.lan:8081"},
			want: []string{"seam.lan"},
		},
		{
			name: "extras first, advertised appended",
			cfg:  Config{Addr: "0.0.0.0:8081", AllowedHosts: []string{"seam", "mac.local"}, AdvertisedURL: "http://seam.lan:8081"},
			want: []string{"seam", "mac.local", "seam.lan"},
		},
		{
			name: "normalized and de-duplicated",
			cfg:  Config{Addr: "0.0.0.0:8081", AllowedHosts: []string{" SEAM.LAN ", "[::1]", "seam.lan", ""}, AdvertisedURL: "http://seam.lan:8081"},
			want: []string{"seam.lan", "::1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.cfg.AllowedHostsEffective())
		})
	}
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
