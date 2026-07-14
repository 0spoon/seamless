package main

// seam ready -- the actionable queue (tasks_ready).

import (
	"context"
	"flag"
	"fmt"
)

func runReady(args []string) error {
	fs := flag.NewFlagSet("ready", flag.ContinueOnError)
	project := fs.String("project", "", "project slug (defaults to server binding)")
	showBlocked := fs.Bool("blocked", false, "also list blocked tasks and their blockers")
	plan := fs.String("plan", "", "show a plan's ready/blocked step tasks instead of the default queue")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	out, err := callTool(ctx, cli, "tasks_ready", map[string]any{"project": *project, "plan": *plan})
	if err != nil {
		return err
	}
	ready, _ := out["ready"].([]any)
	if len(ready) == 0 {
		fmt.Println("ready: (nothing actionable)")
	} else {
		fmt.Printf("ready (%d):\n", len(ready))
		for _, t := range ready {
			m, _ := t.(map[string]any)
			fmt.Printf("  %s  %s\n", shortID(str(m["id"])), str(m["title"]))
		}
	}
	if *showBlocked {
		blocked, _ := out["blocked"].([]any)
		fmt.Printf("blocked (%d):\n", len(blocked))
		for _, t := range blocked {
			m, _ := t.(map[string]any)
			fmt.Printf("  %s  %s\n", shortID(str(m["id"])), str(m["title"]))
			if bl, ok := m["blockers"].([]any); ok {
				for _, b := range bl {
					bm, _ := b.(map[string]any)
					fmt.Printf("      blocked by %s (%s)\n", str(bm["title"]), str(bm["status"]))
				}
			}
		}
	}
	return nil
}
