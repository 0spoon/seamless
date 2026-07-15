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
