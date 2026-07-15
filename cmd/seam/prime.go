package main

// seam prime -- start/resume a session and print its briefing (session_start).

import (
	"context"
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

	out, err := callTool(ctx, cli, "session_start", map[string]any{"cwd": cwd, "name": *o.name, "source": "explicit"})
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
