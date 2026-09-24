package config

import (
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
)

// defaultServerURL is the base URL for an install whose addr cannot be read as
// host:port at all. An addr in that shape is unusable as a listener too
// (net.Listen wants host:port, so the daemon never came up), which is why
// naming the built-in default is more useful than echoing the broken value
// back: every consumer of this URL is a client trying to reach a daemon.
const defaultServerURL = "http://127.0.0.1:8081"

// ServerURL is the base URL (scheme://host[:port], no trailing slash) that
// clients -- the seam CLI, installed hooks, the MCP registration, the console
// opener, the published cards -- use to reach this install's daemon.
//
// It is the single derivation: five copies of "split addr, map the wildcard to
// loopback, prefix http://" used to live in cmd/seam, cmd/seamlessd (twice),
// cmd/seambench and cmd/docsgen, and they had already drifted from each other
// on the malformed-addr edge. Bind address and client target are different
// questions that happen to have the same answer on a loopback install; keeping
// one derivation here is what lets the second question get its own answer
// (server_url) without hunting the first one down in five files.
func (c Config) ServerURL() string {
	scheme := "http"
	if c.TLSEnabled() {
		scheme = "https"
	}
	host, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return defaultServerURL
	}
	return scheme + "://" + net.JoinHostPort(reachableHost(host), port)
}

// ServerHost is the bare host of ServerURL: no scheme, no port, no IPv6
// brackets, lower-cased -- the form a Host header and a TLS SAN carry, and the
// form the Host allowlist compares against.
func (c Config) ServerHost() string {
	u, err := url.Parse(c.ServerURL())
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// IsClient reports whether this install runs no daemon of its own and dials a
// server elsewhere instead.
//
// Always false today: the role key that can make it true lands with the
// transport work. It exists now so the call sites that must branch on it are
// written against one predicate rather than each inventing its own.
func (Config) IsClient() bool {
	return false
}

// TLSEnabled reports whether the daemon serves HTTPS rather than HTTP -- which
// is what decides ServerURL's scheme and whether the console's session cookie
// may be marked Secure.
//
// Always false today: the tls.cert_file/tls.key_file pair that can make it true
// lands with the transport work.
func (Config) TLSEnabled() bool {
	return false
}

// reachableHost maps a bind host that means "every interface" to loopback. A
// wildcard is an answer to "where do I listen", never to "where do I dial":
// advertising http://0.0.0.0:8081 hands a client an address it cannot connect
// to. SplitHostPort already strips IPv6 brackets, so "[::]" only shows up when
// a caller passes a host it parsed itself.
func reachableHost(host string) string {
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	}
	return host
}

var (
	hostnameOnce   sync.Once
	cachedHostname string
)

// Hostname is this machine's hostname, lower-cased and cached for the life of
// the process: it identifies which device a session, a repo mapping, or a
// captured plan came from, so it is read on hot paths (every hook fires one)
// and must not re-syscall each time.
//
// Lower-cased because it is compared with hostnames that arrived over the wire,
// where case is not preserved. An unreadable hostname yields "", which callers
// must treat as "unknown machine" rather than as a name -- never as a stand-in
// for the local one.
func Hostname() string {
	hostnameOnce.Do(func() {
		h, err := os.Hostname()
		if err != nil {
			return // stays "": unknown, not a guess
		}
		cachedHostname = strings.ToLower(strings.TrimSpace(h))
	})
	return cachedHostname
}
