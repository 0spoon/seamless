// Command seam is the headless Seamless CLI. In P2 it speaks JSON-RPC to a
// running seamlessd over /api/mcp for the minimal loop: prime (session_start +
// briefing), remember (memory_write), recall, and status. It loads the same
// config as the server, so it targets the same address and static key.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/config"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "prime":
		err = runPrime(args)
	case "remember":
		err = runRemember(args)
	case "recall":
		err = runRecall(args)
	case "status":
		err = runStatus(args)
	case "sessions":
		err = runSessions(args)
	case "usage":
		err = runUsage(args)
	case "ready":
		err = runReady(args)
	case "task":
		err = runTask(args)
	case "capture":
		err = runCapture(args)
	case "hook":
		err = runHook(args)
	case "doctor":
		err = runDoctor(args)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `seam -- Seamless CLI (talks to a running seamlessd)

agent loop:
  seam prime [--cwd DIR] [--name NAME]         start/resume a session, print the briefing
  seam remember --name N --kind K --description D [--body TEXT] [--project P]
  seam recall QUERY [--scope all|memories|notes] [--project P] [--limit N]
  seam capture URL [--project P]               capture a web page as a note

tasks:
  seam ready [--project P] [--blocked] [--plan S]   actionable queue (+ blocked tasks)
  seam task list [--project P] [--status S] [--plan S]   list tasks (--plan lists a plan's steps)
  seam task add --title T [--body B] [--project P] [--depends id,id] [--plan S]
  seam task done|start|drop|reopen <id>        transition a task
  seam task claim <id> [--lease SECS]          atomically claim a task (lease-based)
  seam task heartbeat <id> [--lease SECS]      refresh the lease on a task you hold
  seam task release <id> [--force]             release a task you hold (--force: owner override, any holder)

observability:
  seam status                                  server health + project count
  seam sessions [--status active|completed]    list sessions (or: seam sessions <id>)
  seam usage                                   activity roll-up
  seam doctor                                  reachability + key + tool-count check

hooks (invoked by Claude Code, not by hand):
  seam hook session-start|user-prompt-submit|session-end   forward the stdin hook payload to seamlessd
  seam hook post-tool-use|subagent-stop|permission-request  plan-mode capture (post-tool-use pre-filters locally)
`)
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
		_ = json.Unmarshal([]byte(text), &out)
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

func runPrime(args []string) error {
	fs := flag.NewFlagSet("prime", flag.ContinueOnError)
	cwd := fs.String("cwd", "", "working directory (default: current)")
	name := fs.String("name", "", "session name (reuse to resume)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			*cwd = wd
		}
	}
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	out, err := callTool(ctx, cli, "session_start", map[string]any{"cwd": *cwd, "name": *name, "source": "explicit"})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "session %v (project %q)\n", out["session_id"], str(out["project"]))
	if b := str(out["briefing"]); b != "" {
		fmt.Println(b)
	} else {
		fmt.Fprintln(os.Stderr, "(no briefing content yet)")
	}
	return nil
}

func runRemember(args []string) error {
	fs := flag.NewFlagSet("remember", flag.ContinueOnError)
	name := fs.String("name", "", "memory name (kebab-case)")
	kind := fs.String("kind", "", "constraint|runbook|protocol|gotcha|decision|refuted|reference|stage")
	desc := fs.String("description", "", "one-line description (<=150 chars)")
	body := fs.String("body", "", "markdown body (default: read stdin)")
	project := fs.String("project", "", "project slug (default: server's binding/global)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *kind == "" || *desc == "" {
		return fmt.Errorf("--name, --kind, and --description are required")
	}
	text := *body
	if text == "" {
		b, _ := io.ReadAll(os.Stdin)
		text = string(b)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("body is empty (pass --body or pipe stdin)")
	}
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	out, err := callTool(ctx, cli, "memory_write", map[string]any{
		"name": *name, "kind": *kind, "description": *desc, "body": text, "project": *project,
	})
	if err != nil {
		return err
	}
	verb := "created"
	if b, _ := out["updated"].(bool); b {
		verb = "updated"
	}
	fmt.Printf("%s memory %q (id %v)\n", verb, *name, out["id"])
	if sim, ok := out["similar"].(map[string]any); ok {
		fmt.Printf("  note: similar to %q (%.2f)\n", str(sim["name"]), num(sim["score"]))
	}
	return nil
}

func runRecall(args []string) error {
	// Parsed manually so flags may appear before or after the query words --
	// agents and owners naturally write "recall <words> --project p".
	scope, project, limit, query := "all", "", 10, ""
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		val := func() string { // value for "--flag value" (empties on "--flag=x" handled below)
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case a == "--scope":
			scope = val()
		case strings.HasPrefix(a, "--scope="):
			scope = strings.TrimPrefix(a, "--scope=")
		case a == "--project":
			project = val()
		case strings.HasPrefix(a, "--project="):
			project = strings.TrimPrefix(a, "--project=")
		case a == "--limit":
			limit = atoiOr(val(), 10)
		case strings.HasPrefix(a, "--limit="):
			limit = atoiOr(strings.TrimPrefix(a, "--limit="), 10)
		default:
			words = append(words, a)
		}
	}
	query = strings.TrimSpace(strings.Join(words, " "))
	if query == "" {
		return fmt.Errorf("usage: seam recall QUERY [--scope all|memories|notes] [--project P] [--limit N]")
	}
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	out, err := callTool(ctx, cli, "recall", map[string]any{
		"query": query, "scope": scope, "project": project, "limit": limit,
	})
	if err != nil {
		return err
	}
	hits, _ := out["hits"].([]any)
	if len(hits) == 0 {
		fmt.Println("no results")
		return nil
	}
	for _, h := range hits {
		m, _ := h.(map[string]any)
		fmt.Printf("[%s] %s (%s, %s %.3f)\n    %s\n",
			str(m["kind"]), str(m["name"]), str(m["age"]), str(m["source"]), num(m["score"]), str(m["description"]))
	}
	return nil
}

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	base := mcpBase(cfg)

	// Health via the unauthenticated /healthz endpoint.
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(base + "/healthz")
	if err != nil {
		return fmt.Errorf("server unreachable at %s: %w", base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var hz map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&hz)
	fmt.Printf("server:   %s (%s)\n", str(hz["status"]), base)
	fmt.Printf("version:  %s\n", str(hz["version"]))
	fmt.Printf("data dir: %s\n", cfg.DataDir)

	// Project count via MCP (also proves the static key works).
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		fmt.Printf("projects: (mcp unavailable: %v)\n", err)
		return nil
	}
	defer func() { _ = cli.Close() }()
	out, err := callTool(ctx, cli, "project_list", nil)
	if err != nil {
		fmt.Printf("projects: (error: %v)\n", err)
		return nil
	}
	ps, _ := out["projects"].([]any)
	slugs := make([]string, 0, len(ps))
	for _, p := range ps {
		if m, ok := p.(map[string]any); ok {
			slugs = append(slugs, str(m["slug"]))
		}
	}
	fmt.Printf("projects: %d [%s]\n", len(slugs), strings.Join(slugs, " "))
	return nil
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
	payload, _ := io.ReadAll(os.Stdin)

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
	_, _ = io.Copy(os.Stdout, resp.Body)
	return nil
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}
