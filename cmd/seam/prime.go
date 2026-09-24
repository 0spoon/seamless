package main

// seam prime -- start/resume a session and print its briefing (session_start).

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

var primeCmd = spec("prime", groupAgentLoop, "start/resume a session, print the briefing",
	noArgs(), bindPrime, runPrime)

type primeOpts struct {
	cwd  *string
	name *string
}

func bindPrime(fs *flag.FlagSet) *primeOpts {
	return &primeOpts{
		cwd:  fs.String("cwd", "", "working `DIR` (default: current)"),
		name: fs.String("name", "", "session `NAME` (reuse to resume)"),
	}
}

func runPrime(ctx context.Context, e *env, o *primeOpts, _ []string) error {
	cwd := *o.cwd
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}
	cli, _, err := e.dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	args := map[string]any{"cwd": cwd, "name": *o.name, "source": "explicit"}
	// The same identity `seam hook` sends, for the same reason: when the daemon
	// is on another machine it cannot resolve this cwd's repository itself, and
	// the session would land in the global scope. Resolved here, where the repo
	// actually is. dial() also sends the host header; passing it explicitly is
	// what survives a transport that drops headers.
	for k, v := range identityParams("session-start", primeIdentityPayload(cwd)) {
		args[primeArgNames[k]] = v
	}
	out, err := callTool(ctx, cli, "session_start", args)
	if err != nil {
		return err
	}
	// The briefing is the product and goes to stdout; the session line is context
	// about it, so it stays on stderr where a pipe will not swallow it.
	fmt.Fprintf(e.stderr, "session %v (project %q)\n", out["session_id"], str(out["project"]))
	if b := str(out["briefing"]); b != "" {
		fmt.Fprintln(e.stdout, b)
	} else {
		fmt.Fprintln(e.stderr, "(no briefing content yet)")
	}
	return nil
}

// primeArgNames maps the hook query keys onto session_start's argument names.
// The two surfaces name the same four values differently (a query param is
// terse, a tool argument is self-describing), and this is the single place that
// knows it.
var primeArgNames = map[string]string{
	"host":      "host",
	"repo_root": "repo_root",
	"main_root": "main_worktree_root",
	"origin":    "repo_origin",
}

// primeIdentityPayload shapes a cwd like the hook body identityParams reads, so
// both surfaces resolve machine identity through one function rather than two
// that drift.
func primeIdentityPayload(cwd string) []byte {
	b, err := json.Marshal(map[string]string{"cwd": cwd})
	if err != nil {
		return nil // identityParams tolerates it: the host alone still travels
	}
	return b
}
