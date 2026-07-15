package main

// seam hook -- forward a Claude Code hook payload (stdin) to seamlessd and copy
// the JSON response back (stdout), so a `command` hook can drive the same server
// logic an `http` hook would. Claude Code runs only command/mcp_tool hooks for
// SessionStart, so this is how the briefing and the ambient session get injected.
//
// Claude Code invokes this command; the owner does not. That one fact shapes
// every decision in this file: a hook must never block the session it serves.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
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
	{"subagent-stop", "/api/hooks/subagent-stop"},
	{"permission-request", "/api/hooks/permission-request"},
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

// hookCmd declares atLeast(0) positionals and no enum, so the table checks
// nothing: every way of misusing hook lands in runHook, which fails open, rather
// than in parse, which cannot. That is deliberate belt-and-braces with usageExit
// (spec.go) -- hook keeps failing at exit 1 even if that exemption is ever lost --
// and it is why the event name is validated in the handler instead of by an enum.
var hookCmd = spec("hook", groupHooks, "forward the stdin hook payload to seamlessd",
	atLeast(0, "EVENT"), bindNoOpts, runHook).
	withLong("events: " + hookEventNames() + `

Claude Code invokes this; it is not run by hand. post-tool-use fires machine-wide
on every Write/Edit, so it is pre-filtered locally: a non-plan file never reaches
the network.

A runtime failure (unreadable stdin, no config, server down) is reported on
stderr and exits 0 -- a hook must never block the session it serves. Only an
unknown event name, which is an install bug rather than a hiccup, is an error.`)

func runHook(ctx context.Context, e *env, _ *noOpts, pos []string) error {
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

	cfg, err := e.loadConfig()
	if err != nil {
		fmt.Fprintln(e.stderr, "seam hook: load config:", err)
		return nil
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
