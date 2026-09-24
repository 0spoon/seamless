package main

// The injectable world a command handler runs against: everything it touches that
// a test cannot supply for itself. Handlers take *env rather than reaching for
// os.Stdout / dial / config.Load directly, so their output and their server can
// both be substituted.
//
// parse deliberately takes none of this. Argument handling must be testable
// without a network or a config file, which is why the parse/execute split is
// also the exit-code boundary (see spec.go).

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"

	"github.com/0spoon/seamless/internal/config"
)

// env is the world a handler is given.
type env struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer

	// dial mirrors the package-level dial (main.go): an initialized MCP client
	// plus the config it was built from, so a handler that needs both does not
	// load config twice.
	dial func(context.Context) (*mcpclient.Client, config.Config, error)

	// loadConfig mirrors config.Load, for the commands that speak to the console
	// JSON surface (client.go) and need the address and key without an MCP
	// session.
	loadConfig func() (config.Config, error)
}

// newEnv returns the real world.
func newEnv() *env {
	return &env{
		stdin:      os.Stdin,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		dial:       dial,
		loadConfig: config.Load,
	}
}

const (
	// dialTimeout bounds how long a client waits to establish a connection. It
	// is separate from the response deadline because a down daemon must fail
	// fast even where the response may legitimately take minutes (mcp-proxy).
	dialTimeout = 5 * time.Second
	// healthTimeout bounds a /healthz probe: it pings SQLite and returns, so a
	// slower answer is a wedged daemon, not a busy one.
	healthTimeout = 3 * time.Second
)

// httpClient is the one HTTP client constructor in this CLI. Every command that
// talks to the daemon -- hook, mcp-proxy, doctor, status, version, the console
// JSON surface, and the MCP dial -- goes through it, because the TLS trust
// decision must be identical on all of them: an https server_url with a private
// CA either verifies everywhere or the CLI half-works in a way that reads as an
// intermittent network fault.
//
// timeout is the whole-request deadline; 0 means none (the mcp-proxy bridge,
// whose tool calls can be LLM-slow). The dial timeout applies either way.
//
// A configured-but-unusable tls.ca_file is an ERROR, never a silent fall back to
// the system pool: the request would then fail at the TLS handshake and look
// exactly like an outage, which is the local-vs-remote distinction AGENTS.md
// requires be kept (llm-degradation-remote-vs-local, same rule).
func httpClient(cfg config.Config, timeout time.Duration) (*http.Client, error) {
	tr := &http.Transport{
		Proxy:       http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{Timeout: dialTimeout}).DialContext,
	}
	if ca := strings.TrimSpace(cfg.TLS.CAFile); ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, fmt.Errorf("tls.ca_file %s: %w", ca, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			// Windows has historically returned an error here; an empty pool
			// plus the configured root is still a working trust store for the
			// one server this CLI talks to.
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tls.ca_file %s: no PEM certificate found", ca)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Timeout: timeout, Transport: tr}, nil
}
