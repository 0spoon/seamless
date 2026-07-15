// Command seam is the headless Seamless CLI. In P2 it speaks JSON-RPC to a
// running seamlessd over /api/mcp for the minimal loop: prime (session_start +
// briefing), remember (memory_write), recall, and status. It loads the same
// config as the server, so it targets the same address and static key.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/config"
)

func main() {
	os.Exit(dispatch(context.Background(), newEnv(), os.Args[1:]))
}

// dispatch resolves argv and returns the process exit code.
//
// A command in the table is parsed by it; everything else still goes to
// legacyDispatch. The invariant that keeps the two honest while both exist: a
// command enters the table exactly when its handler converts, so it is never
// declared in both, and there is no window in which they can disagree.
//
// Exit codes are unchanged here except for --help, which was exiting 1 with
// "error: flag: help requested" because flag.ErrHelp fell through the same funnel
// as a real failure. B6 splits parse (2) from execute (1); today both are 1.
func dispatch(ctx context.Context, e *env, argv []string) int {
	if len(argv) == 0 {
		fmt.Fprint(e.stderr, helpText())
		return 2
	}
	switch argv[0] {
	case "help", "-h", "--help":
		fmt.Fprint(e.stdout, helpText())
		return 0
	}

	c, _, migrated := lookup(commands(), argv)
	if !migrated {
		// A family whose members are in the table is not a legacy command. "task"
		// alone is not dispatchable, but legacyDispatch would call it unknown and
		// dump the whole page; name its subcommands instead. B7 deletes the branch
		// along with legacyDispatch, and parse's own unknownCommand takes over --
		// this is the bridge, not the fix.
		if len(familyOf(commands(), argv[0])) > 0 {
			fmt.Fprintln(e.stderr, "error:", unknownCommand(commands(), argv))
			return 2
		}
		return legacyDispatch(e, argv)
	}
	p, err := parse(commands(), argv)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(e.stdout, commandHelp(*c))
		return 0
	case err != nil:
		fmt.Fprintln(e.stderr, "error:", err)
		fmt.Fprintf(e.stderr, "usage: %s\n", synopsis(*c))
		return 1
	}
	if err := p.cmd.run(ctx, e, p.opts, p.pos); err != nil {
		fmt.Fprintln(e.stderr, "error:", err)
		return 1
	}
	return 0
}

// legacyDispatch runs the commands not yet in the table, preserving exactly the
// switch main used before it. It is scaffolding: each B-track task moves a group
// out of it, and B7 deletes what is left along with the last of the heredoc.
func legacyDispatch(e *env, argv []string) int {
	name, args := argv[0], argv[1:]
	var err error
	switch name {
	case "hook":
		err = runHook(args)
	default:
		fmt.Fprintf(e.stderr, "unknown command %q\n\n", name)
		fmt.Fprint(e.stderr, helpText())
		return 2
	}
	if err != nil {
		fmt.Fprintln(e.stderr, "error:", err)
		return 1
	}
	return 0
}

// mcpBase returns the base URL (scheme://host:port) of the configured server,
// mapping a bind-all host to loopback.
func mcpBase(cfg config.Config) string {
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return "http://127.0.0.1:8081"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// dial loads config and returns an initialized MCP client plus the base URL.
func dial(ctx context.Context) (*mcpclient.Client, config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, cfg, err
	}
	cli, err := mcpclient.NewStreamableHttpClient(mcpBase(cfg)+"/api/mcp",
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + cfg.MCP.APIKey}))
	if err != nil {
		return nil, cfg, err
	}
	if err := cli.Start(ctx); err != nil {
		return nil, cfg, err
	}
	var initReq mcp.InitializeRequest
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "seam-cli", Version: "0"}
	if _, err := cli.Initialize(ctx, initReq); err != nil {
		return nil, cfg, fmt.Errorf("connect to seamlessd at %s: %w", mcpBase(cfg), err)
	}
	return cli, cfg, nil
}

// callTool invokes a tool and returns its decoded JSON result.
func callTool(ctx context.Context, cli *mcpclient.Client, name string, args map[string]any) (map[string]any, error) {
	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
	if err != nil {
		return nil, err
	}
	text := firstText(res)
	if res.IsError {
		return nil, fmt.Errorf("%s", text)
	}
	var out map[string]any
	if text != "" {
		// Every successful tool result is marshalled JSON, so unreadable text means
		// something other than seamlessd answered. Propagate rather than returning a
		// nil map, which callers would render as a confident empty result.
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			return nil, fmt.Errorf("unreadable %s response from seamlessd: %w", name, err)
		}
	}
	return out, nil
}

func firstText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// runHook forwards a Claude Code hook payload (read from stdin) to the matching
// seamlessd hook endpoint and copies the JSON response to stdout, so a `command`
// hook can drive the same server logic an `http` hook would. Claude Code accepts
// only command/mcp_tool hooks for SessionStart, so this is how its briefing and
// ambient session get injected. Runtime failures (server down, bad config) are
// reported to stderr and exit 0 so a hiccup never blocks the session; only a
// misconfigured event name (an install bug) is a hard error.
func runHook(args []string) error {
	endpoints := map[string]string{
		"session-start":      "/api/hooks/session-start",
		"user-prompt-submit": "/api/hooks/user-prompt-submit",
		"session-end":        "/api/hooks/session-end",
		"post-tool-use":      "/api/hooks/post-tool-use",
		"subagent-stop":      "/api/hooks/subagent-stop",
		"permission-request": "/api/hooks/permission-request",
	}
	const events = "session-start|user-prompt-submit|session-end|post-tool-use|subagent-stop|permission-request"
	if len(args) < 1 {
		return fmt.Errorf("usage: seam hook <%s>", events)
	}
	ep, ok := endpoints[args[0]]
	if !ok {
		return fmt.Errorf("unknown hook event %q (want %s)", args[0], events)
	}
	// A stdin failure leaves nothing to forward. Report it and exit 0: a hook
	// must never block the agent (same contract as the request failure below).
	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seam hook: read stdin:", err)
		return nil
	}

	// PostToolUse fires machine-wide on every Write/Edit; drop non-plan events
	// here, before any config load or network round-trip.
	if args[0] == "post-tool-use" && !shouldForwardPostToolUse(payload, defaultPlansDir()) {
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "seam hook: load config:", err)
		return nil // never block the session
	}
	req, err := http.NewRequest(http.MethodPost, mcpBase(cfg)+ep, bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintln(os.Stderr, "seam hook:", err)
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+cfg.MCP.APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seam hook:", err)
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	// Relay whatever arrived; a copy failure means stdout is gone, and a hook
	// must never block the agent by failing here.
	_, _ = io.Copy(os.Stdout, resp.Body) //nolint:errcheck
	return nil
}

// requireFlagsFirst rejects leftover arguments that look like flags. Go's flag
// package stops parsing at the first positional, so "seam capture URL --project p"
// binds the URL and drops --project on the floor -- the command then succeeds
// while silently ignoring what the caller asked for (the note lands in the
// default scope, not the project the dropped flag named).
// No positional this CLI takes can start with "-": URLs are scheme-validated,
// task ids are Crockford base32, and plan slugs carry a "cc-plan-" prefix.
func requireFlagsFirst(fs *flag.FlagSet, usage string) error {
	for _, a := range fs.Args() {
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf("%s: flags must precede the positional argument\n%s", a, usage)
		}
	}
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}
