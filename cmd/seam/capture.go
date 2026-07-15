package main

// seam capture -- SSRF-safe URL capture (capture_url).

import (
	"context"
	"errors"
	"flag"
	"fmt"
)

func runCapture(args []string) error {
	const usage = "usage: seam capture [--project P] URL (flags must precede the URL)"
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	project := fs.String("project", "", "project slug (empty = the session's project; \"global\" files it globally)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New(usage)
	}
	if err := requireFlagsFirst(fs, usage); err != nil {
		return err
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
