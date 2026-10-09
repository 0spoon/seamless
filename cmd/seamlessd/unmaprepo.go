package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arctop/seamless/internal/config"
	"github.com/arctop/seamless/internal/store"
)

// runUnmapRepo removes this machine's repo mappings -- the inverse of map-repo.
// --path removes the mapping for one repo root; --stale removes every mapping
// whose path no longer exists on disk, which is the list doctor's repo map check
// warns about. Like map-repo it writes straight to the database, so no running
// daemon is needed. Only routes go: projects and everything in them stay.
func runUnmapRepo(args []string) error {
	fs := flag.NewFlagSet("unmap-repo", flag.ContinueOnError)
	path := fs.String("path", "", "repo path whose mapping to remove (exact match, made absolute)")
	stale := fs.Bool("stale", false, "remove every mapping on this machine whose path no longer exists on disk")
	dryRun := fs.Bool("dry-run", false, "print what would be removed without changing anything")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("seamlessd.unmap-repo: unexpected argument %q (use --path)", fs.Arg(0))
	}
	if (strings.TrimSpace(*path) == "") == !*stale {
		return fmt.Errorf("seamlessd.unmap-repo: pass exactly one of --path DIR or --stale")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("seamlessd.unmap-repo: %w", err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("seamlessd.unmap-repo: %w", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	targets, err := unmapTargets(ctx, db, *path, *stale)
	if err != nil {
		return fmt.Errorf("seamlessd.unmap-repo: %w", err)
	}
	if len(targets) == 0 {
		fmt.Println("no stale mappings: every path mapped on this machine exists on disk")
		return nil
	}
	if *dryRun {
		for _, r := range targets {
			fmt.Printf("would unmap %s -> project %q\n", r.Path, r.Slug)
		}
		return nil
	}
	paths := make([]string, len(targets))
	for i, r := range targets {
		paths[i] = r.Path
	}
	removed, err := store.RemoveRepoMappings(ctx, db, paths)
	if err != nil {
		return fmt.Errorf("seamlessd.unmap-repo: %w", err)
	}
	for _, r := range removed {
		fmt.Printf("unmapped %s -> project %q\n", r.Path, r.Slug)
	}
	return nil
}

// unmapTargets picks the local mappings an unmap-repo run acts on. With stale it
// is every mapping whose path is provably gone (a clean not-exist, the same test
// doctor applies; any other stat failure keeps the mapping). Otherwise it is the
// mapping for exactly that path, and a path this machine has not mapped is an
// error rather than an empty success.
func unmapTargets(ctx context.Context, db *sql.DB, path string, stale bool) ([]store.RepoMapRow, error) {
	local, err := store.LocalRepoMappings(ctx, db)
	if err != nil {
		return nil, err
	}
	if stale {
		var dead []store.RepoMapRow
		for _, r := range local {
			if _, serr := os.Lstat(r.Path); errors.Is(serr, fs.ErrNotExist) {
				dead = append(dead, r)
			}
		}
		return dead, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	var out []store.RepoMapRow
	for _, r := range local {
		if filepath.Clean(r.Path) == abs {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", store.ErrRepoMappingNotFound, abs)
	}
	return out, nil
}
