package main

// seam fav -- star or unstar an item (memory, note, project, plan, task,
// session, trial). A thin client of the favorite_set MCP tool.

import (
	"context"
	"flag"
	"fmt"
)

var favCmd = spec("fav", groupObservability, "star or unstar an item (favorites sort first and pin into briefings)",
	exactly(2, "kind", "id").withHint("kind is one of memory|note|project|plan|task|session|trial"),
	bindFav, runFav)

type favOpts struct {
	off     *bool
	project *string
}

func bindFav(fs *flag.FlagSet) *favOpts {
	return &favOpts{
		off:     fs.Bool("off", false, "unstar instead of star"),
		project: fs.String("project", "", "project `SLUG` scope for memory-name/note-slug resolution"),
	}
}

func runFav(ctx context.Context, e *env, o *favOpts, pos []string) error {
	cli, _, err := e.dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	out, err := callTool(ctx, cli, "favorite_set", map[string]any{
		"kind": pos[0], "id": pos[1], "favorite": !*o.off, "project": *o.project,
	})
	if err != nil {
		return err
	}
	verb := "starred"
	if fav, _ := out["favorite"].(bool); !fav {
		verb = "unstarred"
	}
	fmt.Fprintf(e.stdout, "%s %s %s\n", verb, str(out["kind"]), str(out["id"]))
	return nil
}
