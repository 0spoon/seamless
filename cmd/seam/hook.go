package main

// seam hook -- forward an agent-client hook payload (stdin) to seamlessd and
// copy the JSON response back (stdout), so a `command` hook can drive the same
// server logic an `http` hook would. Claude Code requires command/mcp_tool hooks
// for SessionStart; Codex uses command hooks for its entire Seamless profile.
//
// Agent clients invoke this command; the owner does not. That one fact shapes
// every decision in this file: a hook must never block the session it serves.

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// hookEvents pairs each event seam forwards with the endpoint it posts to. A
// slice rather than a map: it is the canonical set behind both the validation and
// the help text, and a map would order the events differently on every run.
//
// internal/hooks' seamlessHooks table is what this mirrors. The CLI cannot import
// that package -- it would drag the store, the retriever, and SQLite into a binary
// whose job is one HTTP POST -- so hook_test.go pins this copy against it instead.
// That pin is load-bearing: because a hook fails open, drift here is a silent
// no-op rather than an error.
//
// user-prompt-submit has no command-hook counterpart there (the installer wires
// it as an http hook, which is reliable mid-turn). seam has always accepted it,
// and a hand-wired command hook is a supported thing to have, so it stays.
var hookEvents = []struct{ event, endpoint string }{
	{"session-start", "/api/hooks/session-start"},
	{"user-prompt-submit", "/api/hooks/user-prompt-submit"},
	{"session-end", "/api/hooks/session-end"},
	{"post-tool-use", "/api/hooks/post-tool-use"},
	{"subagent-start", "/api/hooks/subagent-start"},
	{"subagent-stop", "/api/hooks/subagent-stop"},
	{"permission-request", "/api/hooks/permission-request"},
	// Codex-only: its per-turn end signal (heartbeat + provisional harvest). Codex
	// has no SessionEnd, so the codex install profile wires `seam hook stop`.
	{"stop", "/api/hooks/stop"},
}

// hookEndpoint returns the endpoint an event forwards to.
func hookEndpoint(event string) (string, bool) {
	for _, h := range hookEvents {
		if h.event == event {
			return h.endpoint, true
		}
	}
	return "", false
}

// hookEventNames lists the events for help and error text, in table order.
func hookEventNames() string {
	names := make([]string, len(hookEvents))
	for i, h := range hookEvents {
		names[i] = h.event
	}
	return strings.Join(names, ", ")
}

// clientQueryParam is the query key that carries the client discriminator to the
// daemon. It mirrors internal/hooks' unexported constant of the same name (this
// binary must not import that package -- see the file header). Both are the
// literal "client", and the forwarding tests pin the resulting query contract.
const clientQueryParam = "client"

// hookClients is the CLI's thin-binary copy of internal/hooks.HookClients. The
// test-only hooks import pins the two sets exactly; production seam avoids the
// SQLite dependency graph that importing internal/hooks would add.
var hookClients = []string{"claude-code", "codex"}

// hookOpts carries the flags for `seam hook`.
type hookOpts struct {
	config string // --config: abs seamless.yaml the installer bakes in (see bindHook)
	client string // --client: canonical agent CLI discriminator; "" => absent/Claude Code
}

// bindHook registers --config and --client. install-hooks writes --config into
// every command hook so the hook resolves config from any cwd: exec-form command
// hooks carry no environment, so this flag replaces the old SEAMLESS_CONFIG env
// prefix. runHook exports it back to SEAMLESS_CONFIG (config.Load's documented
// override) before loading, keeping every command's cwd-relative search otherwise
// unchanged. --client rides on the forwarded request as ?client=<value> so the
// daemon can pick the right per-client payload adapter; the codex install profile
// sets it, and an omitted flag keeps every Claude Code hook request unchanged.
func bindHook(fs *flag.FlagSet) *hookOpts {
	o := &hookOpts{}
	fs.StringVar(&o.config, "config", "", "path to seamless.yaml, so the hook resolves config from any cwd")
	fs.Var(&enumValue{val: &o.client, valid: hookClients}, "client",
		"agent CLI this hook fires for (`CLIENT`); valid: "+strings.Join(hookClients, ", ")+"; default Claude Code")
	return o
}

// hookCmd declares loose positional arity so event misuse lands in runHook, which
// fails open. --client uses the shared enum flag machinery and usageExit keeps
// that parse error at 1 too: exit 2 would block the agent session.
var hookCmd = spec("hook", groupHooks, "forward the stdin hook payload to seamlessd",
	atLeast(0, "EVENT"), bindHook, runHook).
	withLong("events: " + hookEventNames() + `

Your agent client (Claude Code or Codex) invokes this; it is not run by hand.
post-tool-use is Claude Code only and fires machine-wide
on every Write/Edit, so it is pre-filtered locally: a non-plan file never reaches
the network.

A runtime failure (unreadable stdin, no config, server down) is reported on
stderr and exits 0 -- a hook must never block the session it serves. An unknown
event or client is an install/configuration bug and exits 1, never 2.`)

func runHook(ctx context.Context, e *env, o *hookOpts, pos []string) error {
	// Arity is not enforced by the spec (see hookCmd), so the handler owns the
	// empty case. Both messages name the valid set rather than a "want" blob.
	if len(pos) == 0 {
		return fmt.Errorf("missing hook event: valid values are %s", hookEventNames())
	}
	event := pos[0]
	ep, ok := hookEndpoint(event)
	if !ok {
		return fmt.Errorf("unknown hook event %q: valid values are %s", event, hookEventNames())
	}

	// A stdin failure leaves nothing to forward. Report it and exit 0: a hook
	// must never block the agent (the same contract as every failure below).
	payload, err := io.ReadAll(e.stdin)
	if err != nil {
		fmt.Fprintln(e.stderr, "seam hook: read stdin:", err)
		return nil
	}

	// PostToolUse fires machine-wide on every Write/Edit; drop non-plan events
	// here, before any config load or network round-trip.
	if event == "post-tool-use" && !shouldForwardPostToolUse(payload, defaultPlansDir()) {
		return nil
	}

	// --config is config.Load's documented $SEAMLESS_CONFIG override, moved out of
	// the shell (exec-form hooks carry no env). Setting it in this short-lived hook
	// process is safe and keeps loadConfig's search order the single code path.
	if o.config != "" {
		if err := os.Setenv("SEAMLESS_CONFIG", o.config); err != nil {
			fmt.Fprintln(e.stderr, "seam hook: set config path:", err)
			return nil
		}
	}

	cfg, err := e.loadConfig()
	if err != nil {
		fmt.Fprintln(e.stderr, "seam hook: load config:", err)
		return nil
	}
	// --client rides as ?client=<value> so the daemon selects the per-client
	// payload adapter. Omitted (Claude Code), the URL is byte-identical to before,
	// so existing CC hooks are untouched.
	if o.client != "" {
		ep += "?" + clientQueryParam + "=" + url.QueryEscape(o.client)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpBase(cfg)+ep, bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintln(e.stderr, "seam hook:", err)
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+cfg.MCP.APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(e.stderr, "seam hook:", err)
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	// Relay whatever arrived; a copy failure means stdout is gone, and a hook
	// must never block the agent by failing here.
	_, _ = io.Copy(e.stdout, resp.Body) //nolint:errcheck // a hook must not fail on a broken stdout
	return nil
}
