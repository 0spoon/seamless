package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/store"
)

// runMapRepo upserts an entry in the repo_project_map setting so an agent whose
// working directory is under path is resolved to the given project slug by the
// hooks and by session_start.
func runMapRepo(args []string) error {
	fs := flag.NewFlagSet("map-repo", flag.ContinueOnError)
	path := fs.String("path", "", "absolute repo path (default: current directory)")
	project := fs.String("project", "", "project slug to map the path to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*project) == "" {
		return fmt.Errorf("seamlessd.map-repo: --project is required")
	}

	abs := *path
	if abs == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("seamlessd.map-repo: %w", err)
		}
		abs = wd
	}
	abs, err := filepath.Abs(abs)
	if err != nil {
		return fmt.Errorf("seamlessd.map-repo: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("seamlessd.map-repo: %w", err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("seamlessd.map-repo: %w", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if err := store.AddRepoMapping(ctx, db, abs, *project); err != nil {
		return fmt.Errorf("seamlessd.map-repo: %w", err)
	}
	if _, err := store.EnsureProject(ctx, db, *project, *project); err != nil {
		return fmt.Errorf("seamlessd.map-repo: %w", err)
	}
	fmt.Printf("mapped %s -> project %q\n", abs, *project)
	return nil
}
