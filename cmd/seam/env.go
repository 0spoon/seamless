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
	"io"
	"os"
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

// The HTTP client every command here dials with is config.Config.HTTPClient,
// not a constructor of this CLI's own. It moved to internal/config so that
// seamlessd's client-role surfaces -- install-hooks reading the server's feature
// state, the client-role doctor's live tools/list count -- share the ONE TLS
// trust decision rather than reimplementing it: an https server_url with a
// private CA either verifies everywhere or the install half-works in a way that
// reads as an intermittent network fault. config.DialTimeout is the connection
// deadline it applies; the argument to HTTPClient is the whole-request one
// (0 = none, for the mcp-proxy bridge whose tool calls can be LLM-slow).

// healthTimeout bounds a /healthz probe: it pings SQLite and returns, so a
// slower answer is a wedged daemon, not a busy one.
const healthTimeout = 3 * time.Second
