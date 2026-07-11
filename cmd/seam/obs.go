package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/config"
)

// ---------------------------------------------------------------------------
// seam usage -- roll-up from the usage_summary MCP tool
// ---------------------------------------------------------------------------

func runUsage(args []string) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	out, err := callTool(ctx, cli, "usage_summary", nil)
	if err != nil {
		return err
	}
	mem, _ := out["memories"].(map[string]any)
	ret, _ := out["retrieval"].(map[string]any)
	fmt.Printf("memories: %d active\n", int(num(mem["active"])))
	printCounts("  by kind", mem["byKind"])
	fmt.Printf("notes:    %d\n", int(num(out["notes"])))
	printCounts("sessions", out["sessions"])
	printCounts("tasks", out["tasks"])
	fmt.Printf("retrieval: %d injections, %d reads (%d%% read-after-inject)\n",
		int(num(ret["injections"])), int(num(ret["reads"])), pctOf(num(ret["reads"]), num(ret["injections"])))
	if top, ok := ret["topInjected"].([]any); ok && len(top) > 0 {
		fmt.Println("  most injected:")
		for _, t := range top {
			m, _ := t.(map[string]any)
			fmt.Printf("    %-32s %dx\n", str(m["name"]), int(num(m["count"])))
		}
	}
	printCounts("proposals", out["gardenerPending"])
	return nil
}

// printCounts renders a map[string]number as "label: k=v k=v".
func printCounts(label string, v any) {
	m, _ := v.(map[string]any)
	if len(m) == 0 {
		fmt.Printf("%s: (none)\n", label)
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, int(num(m[k]))))
	}
	fmt.Printf("%s: %s\n", label, strings.Join(parts, " "))
}

// ---------------------------------------------------------------------------
// seam ready -- the actionable queue (tasks_ready)
// ---------------------------------------------------------------------------

func runReady(args []string) error {
	fs := flag.NewFlagSet("ready", flag.ContinueOnError)
	project := fs.String("project", "", "project slug (defaults to server binding)")
	showBlocked := fs.Bool("blocked", false, "also list blocked tasks and their blockers")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	out, err := callTool(ctx, cli, "tasks_ready", map[string]any{"project": *project})
	if err != nil {
		return err
	}
	ready, _ := out["ready"].([]any)
	if len(ready) == 0 {
		fmt.Println("ready: (nothing actionable)")
	} else {
		fmt.Printf("ready (%d):\n", len(ready))
		for _, t := range ready {
			m, _ := t.(map[string]any)
			fmt.Printf("  %s  %s\n", shortID(str(m["id"])), str(m["title"]))
		}
	}
	if *showBlocked {
		blocked, _ := out["blocked"].([]any)
		fmt.Printf("blocked (%d):\n", len(blocked))
		for _, t := range blocked {
			m, _ := t.(map[string]any)
			fmt.Printf("  %s  %s\n", shortID(str(m["id"])), str(m["title"]))
			if bl, ok := m["blockers"].([]any); ok {
				for _, b := range bl {
					bm, _ := b.(map[string]any)
					fmt.Printf("      blocked by %s (%s)\n", str(bm["title"]), str(bm["status"]))
				}
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// seam task -- add / list / transition tasks
// ---------------------------------------------------------------------------

func runTask(args []string) error {
	if len(args) == 0 {
		return runTaskList(nil)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runTaskList(rest)
	case "add":
		return runTaskAdd(rest)
	case "done", "start", "drop", "reopen":
		return runTaskTransition(sub, rest)
	default:
		return fmt.Errorf("unknown task subcommand %q (use: list, add, done, start, drop, reopen)", sub)
	}
}

func runTaskList(args []string) error {
	fs := flag.NewFlagSet("task list", flag.ContinueOnError)
	project := fs.String("project", "", "project slug")
	status := fs.String("status", "", "filter: open|in_progress|done|dropped")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	out, err := callTool(ctx, cli, "tasks_list", map[string]any{"project": *project, "status": *status})
	if err != nil {
		return err
	}
	tasks, _ := out["tasks"].([]any)
	if len(tasks) == 0 {
		fmt.Println("(no tasks)")
		return nil
	}
	for _, t := range tasks {
		m, _ := t.(map[string]any)
		fmt.Printf("  %s  [%-11s] %s\n", shortID(str(m["id"])), str(m["status"]), str(m["title"]))
	}
	return nil
}

func runTaskAdd(args []string) error {
	fs := flag.NewFlagSet("task add", flag.ContinueOnError)
	title := fs.String("title", "", "task title (required)")
	body := fs.String("body", "", "optional details")
	project := fs.String("project", "", "project slug")
	depends := fs.String("depends", "", "comma-separated blocker task ids")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*title) == "" {
		return fmt.Errorf("--title is required")
	}
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	out, err := callTool(ctx, cli, "tasks_add", map[string]any{
		"title": *title, "body": *body, "project": *project, "depends_on": *depends,
	})
	if err != nil {
		return err
	}
	fmt.Printf("added task %s %q\n", shortID(str(out["id"])), str(out["title"]))
	return nil
}

func runTaskTransition(sub string, args []string) error {
	statusFor := map[string]string{"done": "done", "start": "in_progress", "drop": "dropped", "reopen": "open"}
	if len(args) == 0 {
		return fmt.Errorf("usage: seam task %s <id>", sub)
	}
	id := args[0]
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	out, err := callTool(ctx, cli, "tasks_update", map[string]any{"id": id, "status": statusFor[sub]})
	if err != nil {
		return err
	}
	fmt.Printf("task %s -> %s\n", shortID(id), str(out["status"]))
	return nil
}

// ---------------------------------------------------------------------------
// seam capture -- SSRF-safe URL capture (capture_url)
// ---------------------------------------------------------------------------

func runCapture(args []string) error {
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	project := fs.String("project", "", "project slug (empty = inbox)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: seam capture URL [--project P]")
	}
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	out, err := callTool(ctx, cli, "capture_url", map[string]any{"url": fs.Arg(0), "project": *project})
	if err != nil {
		return err
	}
	fmt.Printf("captured %q -> note %s (%s)\n", str(out["title"]), shortID(str(out["id"])), str(out["slug"]))
	return nil
}

// ---------------------------------------------------------------------------
// seam sessions -- session list/detail via the console JSON endpoint
// ---------------------------------------------------------------------------

func runSessions(args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	status := fs.String("status", "", "filter: active|completed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return sessionDetail(cfg, fs.Arg(0))
	}

	var data struct {
		Total    int `json:"total"`
		Active   int `json:"active"`
		Sessions []struct {
			ID       string    `json:"id"`
			Name     string    `json:"name"`
			Project  string    `json:"project"`
			Status   string    `json:"status"`
			Ambient  bool      `json:"ambient"`
			Findings string    `json:"findings"`
			Updated  time.Time `json:"updated"`
		} `json:"sessions"`
	}
	path := "/console/sessions?format=json"
	if *status != "" {
		path += "&status=" + *status
	}
	if err := consoleJSON(cfg, path, &data); err != nil {
		return err
	}
	fmt.Printf("%d sessions (%d active)\n", data.Total, data.Active)
	for _, s := range data.Sessions {
		name := s.Name
		if name == "" {
			name = shortID(s.ID)
		}
		amb := ""
		if s.Ambient {
			amb = " (ambient)"
		}
		fmt.Printf("  %-20s %-10s %-9s %s%s\n", name, orDash(s.Project), s.Status, agoShort(s.Updated), amb)
	}
	return nil
}

func sessionDetail(cfg config.Config, id string) error {
	var d struct {
		Session struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			ProjectSlug string `json:"projectSlug"`
			Status      string `json:"status"`
		} `json:"session"`
		Findings  string `json:"findings"`
		ToolCalls int    `json:"toolCalls"`
		Reads     int    `json:"memoryReads"`
		Writes    int    `json:"memoryWrites"`
		Injected  int    `json:"injectedItems"`
		ReadBack  int    `json:"readAfterInject"`
	}
	if err := consoleJSON(cfg, "/console/sessions/"+id+"?format=json", &d); err != nil {
		return err
	}
	name := d.Session.Name
	if name == "" {
		name = shortID(d.Session.ID)
	}
	fmt.Printf("%s  [%s]  %s\n", name, d.Session.Status, orDash(d.Session.ProjectSlug))
	fmt.Printf("tool calls: %d  writes: %d  reads: %d  read-after-inject: %d/%d\n",
		d.ToolCalls, d.Writes, d.Reads, d.ReadBack, d.Injected)
	if strings.TrimSpace(d.Findings) != "" {
		fmt.Printf("\nfindings:\n%s\n", d.Findings)
	}
	return nil
}

// ---------------------------------------------------------------------------
// seam doctor -- client-side reachability + tool-count check
// ---------------------------------------------------------------------------

// expectedTools mirrors mcp.ToolCount without importing the mcp server package
// into the CLI (which would pull its whole dependency tree). doctor asserts the
// running server exposes this many tools via tools/list.
const expectedTools = 26

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	base := mcpBase(cfg)

	var failed int
	report := func(ok bool, name, detail string) {
		label := "ok"
		if !ok {
			label = "FAIL"
			failed++
		}
		fmt.Printf("  [%-4s] %s: %s\n", label, name, detail)
	}

	// Health.
	client := &http.Client{Timeout: 3 * time.Second}
	resp, herr := client.Get(base + "/healthz")
	if herr != nil {
		report(false, "server", "unreachable at "+base+": "+herr.Error())
		if failed > 0 {
			return fmt.Errorf("doctor: %d check(s) failed", failed)
		}
	} else {
		var hz map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&hz)
		_ = resp.Body.Close()
		report(str(hz["status"]) == "ok", "server", fmt.Sprintf("%s (%s)", str(hz["status"]), base))
	}

	// Key + tool count via MCP tools/list.
	ctx := context.Background()
	cli, _, derr := dial(ctx)
	if derr != nil {
		report(false, "mcp", "connect failed: "+derr.Error())
		return fmt.Errorf("doctor: %d check(s) failed", failed)
	}
	defer func() { _ = cli.Close() }()

	tools, terr := cli.ListTools(ctx, mcp.ListToolsRequest{})
	if terr != nil {
		report(false, "mcp_tools", "tools/list failed (bad key?): "+terr.Error())
	} else {
		n := len(tools.Tools)
		report(n == expectedTools, "mcp_tools", fmt.Sprintf("%d tools (expected %d)", n, expectedTools))
	}

	out, perr := callTool(ctx, cli, "project_list", nil)
	if perr != nil {
		report(false, "projects", perr.Error())
	} else {
		ps, _ := out["projects"].([]any)
		report(true, "projects", fmt.Sprintf("%d registered", len(ps)))
	}

	if failed > 0 {
		return fmt.Errorf("doctor: %d check(s) failed", failed)
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// consoleJSON fetches a console page as JSON, authenticating with the bearer key.
func consoleJSON(cfg config.Config, path string, v any) error {
	req, err := http.NewRequest(http.MethodGet, mcpBase(cfg)+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.MCP.APIKey)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("console unreachable at %s: %w", mcpBase(cfg), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("console returned %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func pctOf(n, d float64) int {
	if d == 0 {
		return 0
	}
	return int(n/d*100 + 0.5)
}

// agoShort renders a compact age for the CLI (mirrors the console's ago helper).
func agoShort(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
