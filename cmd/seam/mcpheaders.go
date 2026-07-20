package main

// seam mcp-headers -- prints the MCP Authorization header as JSON, for Claude
// Code's `headersHelper` hook.
//
// It exists so the bearer key never enters a process argument list. The
// installer used to register the HTTP server with
//
//	claude mcp add ... --header "Authorization: Bearer <key>"
//
// which put the daemon's sole credential in the argv of that subprocess, where
// any other account on the machine could read it out of `ps auxww` for the
// lifetime of the call (audit L4). `headersHelper` names a command Claude Code
// runs at connect time and reads headers from on stdout, so the key travels
// over a pipe instead -- and, as with the Codex mcp-proxy bridge, it is read
// from the 0600 config at the moment it is needed rather than copied into
// another tool's config file.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

// mcpHeadersOpts carries the flags for `seam mcp-headers`.
type mcpHeadersOpts struct {
	config string // --config: abs seamless.yaml the installer bakes into the registration
}

// bindMCPHeaders registers --config, for the same reason mcp-proxy does: the
// client records a command line with no environment, so the config path has to
// travel as a flag rather than through SEAMLESS_CONFIG.
func bindMCPHeaders(fs *flag.FlagSet) *mcpHeadersOpts {
	o := &mcpHeadersOpts{}
	fs.StringVar(&o.config, "config", "", "path to seamless.yaml, so the helper resolves config from any cwd")
	return o
}

var mcpHeadersCmd = spec("mcp-headers", groupBridge, "print the MCP auth header as JSON (Claude Code headersHelper)",
	noArgs(), bindMCPHeaders, runMCPHeaders).
	withLong(`An MCP client spawns this; it is not run by hand. It prints a single JSON
object of HTTP headers on stdout:

  {"Authorization":"Bearer <mcp.api_key>"}

Claude Code runs it at connection time (session start and reconnect) via the
server's headersHelper field, which is what keeps the bearer key out of any
process's argv -- unlike a static --header registration, which stores the key in
the client's config and exposes it in ps during install.

The installer registers it for you:

  claude mcp add-json seamless '{"type":"http","url":"<base>/api/mcp",
    "headersHelper":"<abs seam> mcp-headers --config <abs seamless.yaml>"}' --scope user

Exits nonzero with the reason on stderr if the key is unset, so the client
reports a failed server rather than connecting unauthenticated.`)

func runMCPHeaders(_ context.Context, e *env, o *mcpHeadersOpts, _ []string) error {
	if o.config != "" {
		if err := os.Setenv("SEAMLESS_CONFIG", o.config); err != nil {
			return fmt.Errorf("set config path: %w", err)
		}
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	key := strings.TrimSpace(cfg.MCP.APIKey)
	if key == "" {
		// Fail loudly rather than emitting an empty header: the daemon rejects
		// an empty key anyway, and a silent {} would surface as a confusing
		// unauthorized error at the first tool call instead of here.
		return fmt.Errorf("mcp.api_key is empty; run `seamlessd serve` once to generate it, or set it in seamless.yaml")
	}
	out, err := json.Marshal(map[string]string{"Authorization": "Bearer " + key})
	if err != nil {
		return fmt.Errorf("encode headers: %w", err)
	}
	fmt.Fprintln(e.stdout, string(out))
	return nil
}
