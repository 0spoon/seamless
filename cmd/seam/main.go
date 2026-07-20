// Command seam is the headless Seamless CLI. In P2 it speaks JSON-RPC to a
// running seamlessd over /api/mcp for the minimal loop: prime (session_start +
// briefing), remember (memory_write), recall, and status. It loads the same
// config as the server, so it targets the same address and static key.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"

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
// Every command is in the table now, so this is the whole of it. The exit codes
// are the parse/execute split the table made free:
//
//	--help, -h, help, <cmd> --help   0, to stdout
//	success                          0
//	handler error                    1
//	unparseable command line         c.usageExit() -- 2, except hook (see spec.go)
//
// --help reaching stdout at 0 is itself a fix: flag.ErrHelp used to fall through
// the same funnel as a real failure, so `seam capture --help` printed
// "error: flag: help requested" and exited 1.
func dispatch(ctx context.Context, e *env, argv []string) int {
	if len(argv) == 0 {
		fmt.Fprint(e.stderr, helpText())
		return 2
	}
	switch argv[0] {
	case "help", "-h", "--help":
		fmt.Fprint(e.stdout, helpText())
		return 0
	case "-v", "--version":
		// The flag spellings are rewritten to the command rather than handled
		// here, so all three reach one handler: seamlessd accepts the same three
		// (main.go's "version", "-v", "--version"), and a second implementation
		// is how the two binaries would drift back apart.
		argv = append([]string{"version"}, argv[1:]...)
	}

	c, _, ok := lookup(commands(), argv)
	if !ok {
		// unknownCommand already names a family's members ("task bogus" lists
		// them), so only a word that belongs to no family earns the whole page.
		fmt.Fprintln(e.stderr, "error:", unknownCommand(commands(), argv))
		if len(familyOf(commands(), argv[0])) == 0 {
			fmt.Fprint(e.stderr, helpText())
		}
		return 2
	}
	p, err := parse(commands(), argv)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(e.stdout, commandHelp(*c))
		return 0
	case err != nil:
		fmt.Fprintln(e.stderr, "error:", err)
		fmt.Fprintf(e.stderr, "usage: %s\n", synopsis(*c))
		return c.usageExit()
	}
	if err := p.cmd.run(ctx, e, p.opts, p.pos); err != nil {
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

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}
