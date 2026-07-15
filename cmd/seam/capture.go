package main

// seam capture -- SSRF-safe URL capture (capture_url).

import (
	"context"
	"flag"
	"fmt"
)

func runCapture(args []string) error {
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	project := fs.String("project", "", "project slug (empty = inbox)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: seam capture [--project P] URL (flags must precede the URL)")
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
