package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/store"
	"github.com/0spoon/seamless/internal/validate"
)

// runFamily manages the project_families setting: named groupings whose members
// surface each other's recent findings in briefings (store.SiblingProjects).
// Members are project slugs, not repo paths -- resolve a repo to its slug with
// the repo_project_map (seamlessd map-repo) first.
//
//	seamlessd family list
//	seamlessd family add <name> <slug> [<slug>...]
//	seamlessd family remove <name> [<slug>...]
func runFamily(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("seamlessd.family: usage: family <list|add|remove> ...")
	}
	sub, rest := args[0], args[1:]

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("seamlessd.family: %w", err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("seamlessd.family: %w", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	switch sub {
	case "list":
		return familyList(ctx, db)
	case "add":
		return familyAdd(ctx, db, rest)
	case "remove", "rm":
		return familyRemove(ctx, db, rest)
	default:
		return fmt.Errorf("seamlessd.family: unknown subcommand %q (want list|add|remove)", sub)
	}
}

func familyList(ctx context.Context, db *sql.DB) error {
	families, err := store.ProjectFamilies(ctx, db)
	if err != nil {
		return fmt.Errorf("seamlessd.family: %w", err)
	}
	if len(families) == 0 {
		fmt.Println("no project families configured")
		return nil
	}
	names := make([]string, 0, len(families))
	for name := range families {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		members := append([]string(nil), families[name]...)
		sort.Strings(members)
		fmt.Printf("%s: %s\n", name, strings.Join(members, ", "))
	}
	return nil
}

func familyAdd(ctx context.Context, db *sql.DB, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("seamlessd.family: usage: family add <name> <slug> [<slug>...]")
	}
	name, slugs := args[0], args[1:]
	if err := validate.Name(name); err != nil {
		return fmt.Errorf("seamlessd.family: %w", err)
	}
	if err := warnUnknownSlugs(ctx, db, slugs); err != nil {
		return err
	}
	members, err := store.AddFamilyMembers(ctx, db, name, slugs)
	if err != nil {
		return fmt.Errorf("seamlessd.family: %w", err)
	}
	fmt.Printf("family %q -> %s\n", name, strings.Join(members, ", "))
	return nil
}

func familyRemove(ctx context.Context, db *sql.DB, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("seamlessd.family: usage: family remove <name> [<slug>...]")
	}
	name, slugs := args[0], args[1:]
	members, err := store.RemoveFamilyMembers(ctx, db, name, slugs)
	if err != nil {
		return fmt.Errorf("seamlessd.family: %w", err)
	}
	if len(members) == 0 {
		fmt.Printf("removed family %q\n", name)
		return nil
	}
	fmt.Printf("family %q -> %s\n", name, strings.Join(members, ", "))
	return nil
}

// warnUnknownSlugs prints a stderr warning for each slug that is not yet a
// registered project. It never fails the command: a project may register later
// (an agent opening that repo via session_start), at which point the family
// membership starts taking effect.
func warnUnknownSlugs(ctx context.Context, db *sql.DB, slugs []string) error {
	projects, err := store.ListProjects(ctx, db)
	if err != nil {
		return fmt.Errorf("seamlessd.family: %w", err)
	}
	known := make(map[string]bool, len(projects))
	for _, p := range projects {
		known[p.Slug] = true
	}
	for _, s := range slugs {
		s = strings.TrimSpace(s)
		if s != "" && !known[s] {
			fmt.Fprintf(os.Stderr,
				"warning: %q is not a registered project yet; it takes effect once that repo is opened\n", s)
		}
	}
	return nil
}
