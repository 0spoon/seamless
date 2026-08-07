package main

// seam doctor -- client-side reachability + tool-count check.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/features"
)

// expectedTools mirrors mcp.ToolCount without importing the mcp server package
// into the CLI (which would pull its whole dependency tree). doctor asserts the
// running server exposes this many tools via tools/list.
//
// It is the REGISTERED count, which is not the same as the exposed count:
// optional features (internal/features) ship OFF, and a disabled feature's tools
// are hidden from tools/list. What a given daemon should be exposing is
// expectedTools minus the tools of ITS disabled features, which is why doctor
// reads the effective feature state from the console before judging the number
// (mcpToolsCheck below).
//
// seamlessd's own doctor asserts the same constant against REGISTRATION
// (mcp.NumTools with a zero config), which is feature-independent and correctly
// stays a bare equality -- the two checks measure different things on purpose.
const expectedTools = 33

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
		ok, detail := toolsCheck(cfg, len(tools.Tools))
		report(ok, "mcp_tools", detail)
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

// toolsCheck judges the live tools/list count and renders the mcp_tools detail.
// It reads the daemon's effective optional-feature state first, because with
// optional features defaulting to OFF a fresh install exposes FEWER tools than
// are registered, and that is the common case rather than the exception.
func toolsCheck(cfg config.Config, live int) (bool, string) {
	feats, ferr := consoleFeatures(cfg)
	why := ""
	if ferr != nil {
		why = ferr.Error()
	}
	return toolsVerdict(live, feats, why)
}

// consoleFeatures reads the daemon's effective optional-feature state from the
// console settings JSON -- the state the MCP tool gate actually resolves, file
// and env base plus the console's stored override, rather than whatever this
// machine's config file happens to say.
//
// The field is a POINTER on purpose: a daemon that predates the features
// contract answers without it, and decoding an absent object into a value would
// yield "every optional feature off" -- a plausible dummy indistinguishable from
// a real answer, and one that would lower the expected tool count on a daemon
// exposing all of them. Absent has to stay distinguishable from off.
func consoleFeatures(cfg config.Config) (*config.Features, error) {
	var data struct {
		FeaturesConfig *config.Features `json:"featuresConfig"`
	}
	if err := consoleJSON(cfg, "/console/settings?format=json", &data); err != nil {
		return nil, err
	}
	if data.FeaturesConfig == nil {
		return nil, errors.New("settings JSON carries no featuresConfig")
	}
	return data.FeaturesConfig, nil
}

// toolsVerdict is the pure half of the mcp_tools check: the live count, the
// effective feature state (nil when it could not be read, with why saying so),
// and the registry's own accounting of which tools a disabled feature hides.
func toolsVerdict(live int, feats *config.Features, why string) (bool, string) {
	// Failure-soft. Without the feature state there is no single expected
	// number, only a range: everything registered, minus everything the optional
	// features could be hiding. Judging against the range keeps one unreadable
	// endpoint from failing an otherwise healthy daemon, and naming the reason
	// keeps the line from claiming a certainty it does not have. Asserting the
	// full count instead would fail every default install whose console did not
	// answer; assuming all features off would pretend to know they are.
	if feats == nil {
		low := expectedTools - len(features.ToolOwners())
		return live >= low && live <= expectedTools,
			fmt.Sprintf("%d exposed of %d registered, expected %d-%d (feature state unreadable: %s)",
				live, expectedTools, low, expectedTools, why)
	}
	hidden := features.HiddenTools(*feats)
	if len(hidden) == 0 {
		return live == expectedTools, fmt.Sprintf("%d tools (expected %d)", live, expectedTools)
	}
	want := expectedTools - len(hidden)
	names := disabledFeatureNames(*feats)
	if live == want {
		return true, fmt.Sprintf("%d registered, %d exposed (%s disabled)", expectedTools, live, names)
	}
	return false, fmt.Sprintf("%d registered, %d exposed, expected %d with %s disabled",
		expectedTools, live, want, names)
}

// disabledFeatureNames lists, in registry order, the disabled features that
// account for a gap between the registered and exposed tool counts. Features
// that own no tools are left out: they explain nothing about this number.
func disabledFeatureNames(c config.Features) string {
	var keys []string
	for _, f := range features.Registry() {
		if f.Enabled(c) || len(f.Tools) == 0 {
			continue
		}
		keys = append(keys, string(f.Key))
	}
	return strings.Join(keys, ", ")
}
