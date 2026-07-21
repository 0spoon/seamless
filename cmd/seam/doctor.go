package main

// seam doctor -- client-side reachability + tool-count check.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// expectedTools mirrors mcp.ToolCount without importing the mcp server package
// into the CLI (which would pull its whole dependency tree). doctor asserts the
// running server exposes this many tools via tools/list.
const expectedTools = 31

var doctorCmd = spec("doctor", groupObservability, "reachability + key + tool-count check",
	noArgs(), bindNoOpts, runDoctor)

func runDoctor(ctx context.Context, e *env, _ *noOpts, _ []string) error {
	cfg, err := e.loadConfig()
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
		fmt.Fprintf(e.stdout, "  [%-4s] %s: %s\n", label, name, detail)
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
		derr := json.NewDecoder(resp.Body).Decode(&hz)
		_ = resp.Body.Close()
		if derr != nil {
			// Without this the check still fails, but reports a blank status and no reason.
			report(false, "server", "unreadable health response from "+base+": "+derr.Error())
		} else {
			report(str(hz["status"]) == "ok", "server", fmt.Sprintf("%s (%s)", str(hz["status"]), base))
		}
	}

	// Key + tool count via MCP tools/list.
	cli, _, derr := e.dial(ctx)
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
