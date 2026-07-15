package main

// seam remember -- write a memory (memory_write).

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/0spoon/seamless/internal/core"
)

// The required flags are named in the summary because the synopsis cannot say it:
// every flag renders as optional, and the spec table has no notion of a required
// flag (the handler checks them). Saying it here beats inventing a mechanism for
// one command.
var rememberCmd = spec("remember", groupAgentLoop,
	"write a memory (--name, --kind, --description required)",
	noArgs(), bindRemember, runRemember).
	withLong("The body is read from stdin unless --body is given:\n" +
		"  seam remember --name x --kind gotcha --description \"...\" < notes.md")

type rememberOpts struct {
	name    *string
	kind    *string
	desc    *string
	body    *string
	project *string
}

func bindRemember(fs *flag.FlagSet) *rememberOpts {
	return &rememberOpts{
		name: fs.String("name", "", "memory `NAME` (kebab-case)"),
		// Derived from the canonical set, not transcribed: this help string used to
		// be a hand-written copy of core.MemoryKinds ("constraint|runbook|...") with
		// nothing binding the two together.
		kind:    enumFlag(fs, "kind", "", "memory `KIND`", enumOf(core.MemoryKinds)),
		desc:    fs.String("description", "", "one-line `DESC` (<=150 chars)"),
		body:    fs.String("body", "", "markdown `TEXT` (default: read stdin)"),
		project: fs.String("project", "", "project `SLUG` (default: server's binding/global)"),
	}
}

func runRemember(ctx context.Context, e *env, o *rememberOpts, _ []string) error {
	if *o.name == "" || *o.kind == "" || *o.desc == "" {
		return fmt.Errorf("--name, --kind, and --description are required")
	}
	text := *o.body
	if text == "" {
		// Report a read failure as itself; discarding it would surface as the
		// "body is empty" guard below and send the user hunting for a pipe
		// that was in fact there.
		b, err := io.ReadAll(e.stdin)
		if err != nil {
			return fmt.Errorf("read body from stdin: %w", err)
		}
		text = string(b)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("body is empty (pass --body or pipe stdin)")
	}
	cli, _, err := e.dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	out, err := callTool(ctx, cli, "memory_write", map[string]any{
		"name": *o.name, "kind": *o.kind, "description": *o.desc, "body": text, "project": *o.project,
	})
	if err != nil {
		return err
	}
	verb := "created"
	if b, _ := out["updated"].(bool); b {
		verb = "updated"
	}
	fmt.Fprintf(e.stdout, "%s memory %q (id %v)\n", verb, *o.name, out["id"])
	if sim, ok := out["similar"].(map[string]any); ok {
		fmt.Fprintf(e.stdout, "  note: similar to %q (%.2f)\n", str(sim["name"]), num(sim["score"]))
	}
	return nil
}
