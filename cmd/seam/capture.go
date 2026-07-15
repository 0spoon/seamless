package main

// seam capture -- SSRF-safe URL capture (capture_url).

import (
	"context"
	"flag"
	"fmt"
)

var captureCmd = spec("capture", groupAgentLoop, "capture a web page as a note",
	exactly(1, "url"), bindCapture, runCapture)

type captureOpts struct {
	project *string
}

func bindCapture(fs *flag.FlagSet) *captureOpts {
	return &captureOpts{
		project: fs.String("project", "", "project `SLUG` (empty = the session's project; \"global\" files it globally)"),
	}
}

func runCapture(ctx context.Context, e *env, o *captureOpts, pos []string) error {
	cli, _, err := e.dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	out, err := callTool(ctx, cli, "capture_url", map[string]any{"url": pos[0], "project": *o.project})
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "captured %q -> note %s (%s)\n", str(out["title"]), shortID(str(out["id"])), str(out["slug"]))
	return nil
}
