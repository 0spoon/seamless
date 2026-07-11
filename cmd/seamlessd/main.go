// Command seamlessd is the Seamless server daemon and operator CLI.
//
// Subcommands (grown phase by phase per docs/PLAN.md):
//
//	seamlessd serve     start the HTTP server
//	seamlessd doctor    run configuration + database self-checks
//	seamlessd import     import a Seam v1 data directory
//	seamlessd version   print the version
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/console"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/gardener"
	"github.com/0spoon/seamless/internal/hooks"
	"github.com/0spoon/seamless/internal/llm"
	"github.com/0spoon/seamless/internal/mcp"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

// version is the seamlessd build version. Bumped at release; "dev" until P6.
const version = "0.0.0-dev"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "serve":
		err = runServe(args)
	case "doctor":
		err = runDoctor(args)
	case "import":
		err = runImport(args)
	case "install-hooks":
		err = runInstallHooks(args)
	case "map-repo":
		err = runMapRepo(args)
	case "version", "-v", "--version":
		fmt.Printf("seamlessd %s\n", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		slog.Error("command failed", "cmd", cmd, "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `seamlessd %s -- Seamless server daemon

usage:
  seamlessd serve          start the HTTP server (127.0.0.1:8081)
  seamlessd doctor         run configuration + database self-checks
  seamlessd import         import a Seam v1 data directory (--from ~/.seam)
  seamlessd install-hooks  install the SessionStart/UserPromptSubmit hooks
  seamlessd map-repo       map a repo path to a project slug (repo_project_map)
  seamlessd version        print the version
`, version)
}

// runServe starts the HTTP server and blocks until SIGINT/SIGTERM, then shuts
// down gracefully. It wires /healthz, the MCP tool endpoint at /api/mcp, the
// SessionStart/UserPromptSubmit/SessionEnd hooks under /api/hooks, and the
// server-rendered observability console under /console.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "", "HTTP bind address (overrides config)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("seamlessd.serve: %w", err)
	}
	bind := cfg.Addr
	if *addr != "" {
		bind = *addr
	}
	logger := slog.Default()

	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("seamlessd.serve: %w", err)
	}
	defer func() { _ = db.Close() }()
	if v, verr := store.SchemaVersion(db); verr == nil {
		slog.Info("database ready", "path", cfg.DBPath(), "schema_version", v)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Files subsystem: markdown source of truth, watcher, reconcile, and
	// embed-on-index when an embedder is configured.
	mgr, err := files.NewManager(cfg.DataDir, db, logger)
	if err != nil {
		return fmt.Errorf("seamlessd.serve: %w", err)
	}
	defer func() { _ = mgr.Close() }()

	var embedder llm.Embedder
	if e, eerr := llm.NewEmbedder(cfg.LLM); eerr != nil {
		slog.Warn("embeddings disabled; recall degrades to FTS", "err", eerr)
	} else {
		embedder = e
		mgr.SetEmbedder(embedder)
		slog.Info("embeddings enabled", "provider", cfg.LLM.Provider, "model", embedder.Model())
	}
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("seamlessd.serve: files start: %w", err)
	}

	ret := retrieve.New(db, embedder, cfg.Budgets, logger)
	ret.SetBodyReader(mgr.Store()) // enables the pinned-stage briefing section
	rec := events.NewRecorder(db)

	// Gardener: propose-only maintenance, exposed to the gardener_apply MCP tool
	// and run on a ticker. The chat client (for digests) is best-effort; without
	// it the digest pass simply no-ops.
	var chat llm.Chat
	if c, cerr := llm.NewChatClient(cfg.LLM); cerr != nil {
		slog.Warn("gardener digests disabled; chat client unavailable", "err", cerr)
	} else {
		chat = c
	}
	garden := gardener.New(db, mgr, embedder, chat, rec, gardener.FromConfig(cfg.Gardener), logger)

	if cfg.MCP.APIKey == "" {
		slog.Warn("mcp.api_key is empty; MCP and hook requests will be rejected -- set it in seamless.yaml")
	}
	mcpSrv := mcp.New(mcp.Config{
		DB: db, Files: mgr, Retrieve: ret, Events: rec, Gardener: garden, Embedder: embedder,
		APIKey: cfg.MCP.APIKey, Logger: logger,
	})
	hooksH := hooks.NewHandler(db, ret, rec, cfg.MCP.APIKey, logger)
	consoleSrv, err := console.New(console.Config{
		DB: db, Files: mgr, Gardener: garden, Events: rec,
		APIKey: cfg.MCP.APIKey, DataDir: cfg.DataDir, Budgets: cfg.Budgets, Logger: logger,
	})
	if err != nil {
		return fmt.Errorf("seamlessd.serve: console: %w", err)
	}

	if cfg.Gardener.Enabled {
		garden.Start(ctx)
		slog.Info("gardener enabled", "interval", time.Duration(cfg.Gardener.IntervalMinutes)*time.Minute,
			"dedup_threshold", cfg.Gardener.DedupThreshold, "staleness_days", cfg.Gardener.StalenessDays)
	} else {
		slog.Info("gardener disabled")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthzHandler(db))
	mux.Handle("/api/mcp", mcpSrv.Handler())
	hooksH.Register(mux)
	consoleSrv.Register(mux)

	srv := &http.Server{
		Addr:              bind,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("seamlessd listening", "addr", bind, "data_dir", cfg.DataDir,
			"version", version, "mcp_tools", mcp.ToolCount)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err := <-errCh:
		return fmt.Errorf("seamlessd.serve: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("seamlessd.serve: shutdown: %w", err)
	}
	slog.Info("seamlessd stopped")
	return nil
}

// healthzHandler reports liveness plus a database ping.
func healthzHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, code := "ok", http.StatusOK
		if err := db.PingContext(r.Context()); err != nil {
			status, code = "degraded", http.StatusServiceUnavailable
			slog.Warn("healthz: db ping failed", "err", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if err := json.NewEncoder(w).Encode(map[string]string{"status": status, "version": version}); err != nil {
			slog.Warn("healthz: encode response", "err", err)
		}
	}
}

// runDoctor runs self-checks. P0 fleshes this out step by step; later steps add
// config loading and database checks. See doctor.go.
func runDoctor(args []string) error {
	return doctor(args)
}
