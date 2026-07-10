package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/importer"
	"github.com/0spoon/seamless/internal/llm"
	"github.com/0spoon/seamless/internal/store"
)

// runImport migrates a Seam v1 data directory into this Seamless instance.
// Memory/note files are written and indexed under the v2 data dir; trials,
// sessions, and tool-call events are inserted into seam.db. It is idempotent by
// id, so re-running it imports only what is new (P6 delta re-import).
func runImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	from := fs.String("from", "~/.seam", "v1 data directory to import from")
	skip := fs.String("skip", "briefings", "comma-separated storage projects to skip")
	embed := fs.Bool("embed", true, "embed imported items for cosine search (uses the configured provider)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	src, err := expandHome(*from)
	if err != nil {
		return fmt.Errorf("seamlessd.import: %w", err)
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("seamlessd.import: source %s: %w", src, err)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("seamlessd.import: %w", err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("seamlessd.import: %w", err)
	}
	defer func() { _ = db.Close() }()

	mgr, err := files.NewManager(cfg.DataDir, db, nil)
	if err != nil {
		return fmt.Errorf("seamlessd.import: %w", err)
	}
	defer func() { _ = mgr.Close() }()

	if *embed {
		embedder, eerr := llm.NewEmbedder(cfg.LLM)
		if eerr != nil {
			fmt.Fprintf(os.Stderr, "warning: embeddings disabled (%v); importing without vectors\n", eerr)
		} else {
			mgr.SetEmbedder(embedder)
			fmt.Fprintf(os.Stderr, "embedding via %s/%s (this may take a minute)...\n", cfg.LLM.Provider, embedder.Model())
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	opts := importer.Options{SourceDir: src, SkipProjects: splitCSV(*skip)}
	fmt.Fprintf(os.Stderr, "importing from %s into %s ...\n", src, cfg.DataDir)
	rep, err := importer.Import(ctx, mgr, db, opts)
	if rep != nil {
		fmt.Println(rep.String())
	}
	if err != nil {
		return fmt.Errorf("seamlessd.import: %w", err)
	}
	return nil
}

// splitCSV splits a comma-separated list, trimming spaces and dropping blanks.
func splitCSV(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// expandHome expands a leading ~ to the user's home directory.
func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}
