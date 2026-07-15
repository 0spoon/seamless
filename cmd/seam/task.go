package main

// seam task -- add / list / transition / claim / release tasks.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/0spoon/seamless/internal/config"
)

func runTask(args []string) error {
	if len(args) == 0 {
		return runTaskList(nil)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runTaskList(rest)
	case "add":
		return runTaskAdd(rest)
	case "done", "start", "drop", "reopen":
		return runTaskTransition(sub, rest)
	case "claim", "heartbeat":
		return runTaskClaim(sub, rest)
	case "release":
		return runTaskRelease(rest)
	default:
		return fmt.Errorf("unknown task subcommand %q (use: list, add, done, start, drop, reopen, claim, heartbeat, release)", sub)
	}
}

func runTaskList(args []string) error {
	fs := flag.NewFlagSet("task list", flag.ContinueOnError)
	id := fs.String("id", "", "load a single task by its globally-unique id (ignores project/status/plan)")
	project := fs.String("project", "", "project slug")
	status := fs.String("status", "", "filter: open|in_progress|done|dropped")
	plan := fs.String("plan", "", "list a plan's step tasks instead of the default (non-plan) tasks")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	out, err := callTool(ctx, cli, "tasks_list", map[string]any{"id": *id, "project": *project, "status": *status, "plan": *plan})
	if err != nil {
		return err
	}
	tasks, _ := out["tasks"].([]any)
	if len(tasks) == 0 {
		fmt.Println("(no tasks)")
		return nil
	}
	for _, t := range tasks {
		m, _ := t.(map[string]any)
		fmt.Printf("  %s  [%-11s] %s\n", shortID(str(m["id"])), str(m["status"]), str(m["title"]))
	}
	return nil
}

func runTaskAdd(args []string) error {
	fs := flag.NewFlagSet("task add", flag.ContinueOnError)
	title := fs.String("title", "", "task title (required)")
	body := fs.String("body", "", "optional details")
	project := fs.String("project", "", "project slug")
	depends := fs.String("depends", "", "comma-separated blocker task ids")
	plan := fs.String("plan", "", "plan slug: compose this task as a step of a plan (plan:<slug>)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*title) == "" {
		return fmt.Errorf("--title is required")
	}
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	out, err := callTool(ctx, cli, "tasks_add", map[string]any{
		"title": *title, "body": *body, "project": *project, "depends_on": *depends, "plan": *plan,
	})
	if err != nil {
		return err
	}
	fmt.Printf("added task %s %q\n", shortID(str(out["id"])), str(out["title"]))
	return nil
}

func runTaskTransition(sub string, args []string) error {
	statusFor := map[string]string{"done": "done", "start": "in_progress", "drop": "dropped", "reopen": "open"}
	if len(args) == 0 {
		return fmt.Errorf("usage: seam task %s <id>", sub)
	}
	id := args[0]
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	out, err := callTool(ctx, cli, "tasks_update", map[string]any{"id": id, "status": statusFor[sub]})
	if err != nil {
		return err
	}
	fmt.Printf("task %s -> %s\n", shortID(id), str(out["status"]))
	return nil
}

// runTaskClaim backs both `task claim` and `task heartbeat`: both call tasks_claim,
// which claims a ready task or, when the caller already holds it, refreshes the lease.
func runTaskClaim(sub string, args []string) error {
	usageMsg := fmt.Sprintf("usage: seam task %s [--lease <seconds>] <id> (flags must precede the id)", sub)
	fs := flag.NewFlagSet("task "+sub, flag.ContinueOnError)
	lease := fs.Int("lease", 0, "lease seconds before the claim lapses (default: server default of 900)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New(usageMsg)
	}
	if err := requireFlagsFirst(fs, usageMsg); err != nil {
		return err
	}
	id := fs.Arg(0)
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	call := map[string]any{"id": id}
	if *lease > 0 {
		call["lease_seconds"] = strconv.Itoa(*lease)
	}
	out, err := callTool(ctx, cli, "tasks_claim", call)
	if err != nil {
		return err
	}
	fmt.Printf("task %s %s -> %s (lease until %s)\n", shortID(id), sub, str(out["status"]), str(out["lease_expires_at"]))
	return nil
}

func runTaskRelease(args []string) error {
	const usageMsg = "usage: seam task release [--force] <id> (flags must precede the id)"
	fs := flag.NewFlagSet("task release", flag.ContinueOnError)
	force := fs.Bool("force", false, "owner override: release the lock even if you do not hold it (routes through the console owner surface, not the agent claim path)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New(usageMsg)
	}
	if err := requireFlagsFirst(fs, usageMsg); err != nil {
		return err
	}
	id := fs.Arg(0)

	// --force is the owner override: it force-releases any holder's claim via the
	// console POST route (bearer-authenticated), which agents cannot reach. The
	// plain path is holder-checked: tasks_release only releases a claim you hold.
	if *force {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		var out struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := consolePOST(cfg, "/console/tasks/"+id+"/release?format=json", &out); err != nil {
			return err
		}
		fmt.Printf("task %s force-released -> %s\n", shortID(id), out.Status)
		return nil
	}

	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	out, err := callTool(ctx, cli, "tasks_release", map[string]any{"id": id})
	if err != nil {
		return err
	}
	fmt.Printf("task %s released -> %s\n", shortID(id), str(out["status"]))
	return nil
}
