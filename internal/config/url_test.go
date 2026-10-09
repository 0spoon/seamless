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

// Every spelling of the unspecified address is the same wildcard bind, so the
// derive path rewrites all of them -- before IsWildcardHost, "[::0]:9000"
// advertised http://[::0]:9000 back to clients. The non-default port is what
// shows the rewrite fired: the malformed-addr fallback would have said 8081.
func TestServerURL_EverySpellingOfTheWildcardDerivesLoopback(t *testing.T) {
	tests := []struct{ name, addr, want string }{
		{"compressed zero", "[::0]:9000", "http://127.0.0.1:9000"},
		{"fully expanded", "[0:0:0:0:0:0:0:0]:9000", "http://127.0.0.1:9000"},
		{"zero-padded", "[0000:0000:0000:0000:0000:0000:0000:0000]:9000", "http://127.0.0.1:9000"},
		{"ipv4-mapped unspecified", "[::ffff:0.0.0.0]:9000", "http://127.0.0.1:9000"},
		{"zoned unspecified", "[::%lo0]:9000", "http://127.0.0.1:9000"},

		// Unbracketed, an expanded IPv6 wildcard is not host:port at all:
		// SplitHostPort reports too many colons and net.Listen refuses it the
		// same way, so it takes the malformed-addr fallback (port and all),
		// never the wildcard rewrite.
		{"unbracketed expanded", "0:0:0:0:0:0:0:0:9000", "http://127.0.0.1:8081"},

		// A name is echoed, even one the libc resolver happens to read as
		// 0.0.0.0: IsWildcardHost judges literals only.
		{"zero as a name", "0:9000", "http://0:9000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Config{Addr: tt.addr}.ServerURL())
		})
	}
	require.Equal(t, "127.0.0.1", Config{Addr: "[::0]:9000"}.ServerHost())
}

// IsWildcardHost is the one definition shared by the derive path, the
// server_url refusal and the daemon's bind guards, so its edges are pinned
// here rather than at any one of them.
func TestIsWildcardHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		// The bare-port bind (":8081") splits to an empty host.
		{"", true},
		{"[]", true},

		{"0.0.0.0", true},
		{"::", true},
		{"[::]", true}, // a host a caller split itself keeps its brackets
		{"::0", true},
		{"[::0]", true},
		{"0:0::0", true},
		{"0:0:0:0:0:0:0:0", true},
		{"0000:0000:0000:0000:0000:0000:0000:0000", true},
		{"::0.0.0.0", true},
		{"::ffff:0.0.0.0", true}, // IPv4-mapped, as net.IP.IsUnspecified reads it
		{"[::ffff:0.0.0.0]", true},
		{"::%lo0", true}, // net.Listen ignores the zone and binds every interface
		{"[::%en0]", true},

		{"127.0.0.1", false},
		{"::1", false},
		{"[::1]", false},
		{"::ffff:127.0.0.1", false},
		{"fe80::1%lo0", false}, // zoned, but not unspecified
		{"fd00::1", false},
		{"192.168.1.5", false},
		{"localhost", false},
		{"example.com", false},

		// Names, not literals: Go refuses the leading zeros, and only the
		// libc resolver reads these as 0.0.0.0. No lookup on the config path.
		{"0", false},
		{"00.0.0.0", false},
		{"0.0.0.00", false},
		{"0.0.0", false},
		{"0.0.0.0%en0", false}, // an IPv4 literal takes no zone
		{"[::", false},         // unbalanced brackets are not a host
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			require.Equal(t, tt.want, IsWildcardHost(tt.host))
			if tt.want {
				require.Equal(t, "127.0.0.1", reachableHost(tt.host), "the derive path rewrites exactly what the predicate calls a wildcard")
			} else {
				require.Equal(t, tt.host, reachableHost(tt.host), "and leaves everything else alone")
			}
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

		// Not literal matches for any spelling above, and just as undialable
		// from another machine: the refusal judges the address.
		{"http://[::0]:8081", "::0"},
		{"http://[0:0:0:0:0:0:0:0]:8081", "0:0:0:0:0:0:0:0"},
		{"http://[::ffff:0.0.0.0]:8081", "::ffff:0.0.0.0"},
		{"http://[::%25lo0]:8081", "::%lo0"},
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
		"an empty host is a wildcard to IsWildcardHost, but the earlier and more specific error wins")
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
