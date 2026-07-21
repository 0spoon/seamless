// Command seamlessd is the Seamless server daemon and operator CLI.
//
// Subcommands:
//
//	seamlessd serve         start the HTTP server
//	seamlessd doctor        run configuration + database self-checks
//	seamlessd import        import a Seam v1 data directory
//	seamlessd install-hooks install agent hooks, MCP, and maintained skills
//	seamlessd uninstall     remove Seamless (service, hooks, MCP, skills, binaries)
//	seamlessd update        upgrade in place to the latest release (re-runs the installer)
//	seamlessd map-repo      override a repo's auto-derived project slug (rarely needed)
//	seamlessd family        manage project families
//	seamlessd console-open  open the console in a browser, pre-authenticated
//	seamlessd start         start|stop|restart|status the installed service
//	seamlessd version       print the version
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
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

// version is the seamlessd build version, injected from the git tag at build
// time: goreleaser sets it via -X main.version (see .goreleaser.yaml) and so
// does `make build`/`make install` (see the Makefile's VERSION). The git tag is
// the single source of truth -- this default is only reached by a plain
// `go build`/`go test` with no ldflags, so it is a non-release dev sentinel
// (never a real version number, which would silently go stale here).
var version = "0.0.0-dev"

// commit and buildDate are link-time build metadata, set via
//
//	go build -ldflags "-X main.commit=$(git rev-parse --short HEAD) -X main.buildDate=<utc>"
//
// (see the Makefile and .goreleaser.yaml). They stay "unknown" for a plain
// `go build`/`go test`.
var (
	commit    = "unknown"
	buildDate = "unknown"
)

// buildVersion is the version plus the short commit when linked in, e.g.
// "0.0.0-dev+1a2b3c4". It is surfaced in /healthz, the MCP handshake, and the
// startup log so a stale running daemon (older code than the working tree) is
// visible at a glance.
func buildVersion() string {
	if commit == "unknown" {
		return version
	}
	return version + "+" + commit
}

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
	case "install-summary":
		err = runInstallSummary(args)
	case "uninstall":
		err = runUninstall(args)
	case "update":
		err = runUpdate(args)
	case "map-repo":
		err = runMapRepo(args)
	case "family":
		err = runFamily(args)
	case "console-open":
		err = runConsoleOpen(args)
	case "start":
		err = runServiceAction(actionStart, args)
	case "stop":
		err = runServiceAction(actionStop, args)
	case "restart":
		err = runServiceAction(actionRestart, args)
	case "status":
		err = runServiceAction(actionStatus, args)
	case "version", "-v", "--version":
		fmt.Printf("seamlessd %s (commit %s, built %s)\n", version, commit, buildDate)
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
  seamlessd install-hooks  install Claude Code/Codex hooks, MCP, and maintained skills
  seamlessd uninstall      remove Seamless: service, hooks, MCP, skills, binaries
                           (--purge also deletes config + ~/.seamless; --dry-run to preview)
  seamlessd update         upgrade in place to the latest release by re-running the installer
                           (--check reports installed vs latest; --dry-run previews; honors SEAMLESS_VERSION)
  seamlessd map-repo       override a repo's auto-derived project slug (rarely needed;
                           repos self-map on first session -- repo_project_map)
  seamlessd family         manage project families (list|add|remove)
  seamlessd console-open   open the console in a browser, pre-authenticated
                           (--browser "Google Chrome" targets a specific browser; macOS only)
  seamlessd start          start the installed service (launchd/systemd/Scheduled Task)
  seamlessd stop           stop the installed service
  seamlessd restart        restart the installed service in place
  seamlessd status         print the installed service's state
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
	cfgPath := fs.String("config", "", "path to seamless.yaml (overrides $SEAMLESS_CONFIG and the search path)")
	logFile := fs.String("log-file", "", "append logs to this file in addition to stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}

	closeLog, err := applyServeEnv(*cfgPath, *logFile)
	if err != nil {
		return err
	}
	defer closeLog()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("seamlessd.serve: %w", err)
	}
	var keyPath string
	cfg, keyPath, err = config.EnsureAPIKey(cfg)
	if err != nil {
		return fmt.Errorf("seamlessd.serve: %w", err)
	}
	if keyPath != "" {
		slog.Info("first run: generated mcp.api_key", "config", keyPath)
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
		// Separate a deliberate opt-out (no credential) from a mistake (a
		// malformed base_url). Both leave recall lexical-only for the life of
		// the process, and that is the only other symptom the owner ever sees,
		// so a typo says so at Error rather than blending into the warnings.
		if errors.Is(eerr, llm.ErrConfig) {
			slog.Error("embeddings disabled by a misconfiguration; recall stays FTS-only until it is fixed", "err", eerr)
		} else {
			slog.Warn("embeddings disabled; recall degrades to FTS", "err", eerr)
		}
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
	ret.SetBriefingConfig(cfg.Briefing)
	ret.SetSearchConfig(cfg.Search)
	rec := events.NewRecorder(db)

	// Gardener: propose-only maintenance, exposed to the gardener_apply MCP tool
	// and run on a ticker. The chat client (for digests) is best-effort; without
	// it the digest pass simply no-ops.
	var chat llm.Chat
	if c, cerr := llm.NewChatClient(cfg.LLM); cerr != nil {
		if errors.Is(cerr, llm.ErrConfig) {
			slog.Error("gardener digests disabled by a misconfiguration", "err", cerr)
		} else {
			slog.Warn("gardener digests disabled; chat client unavailable", "err", cerr)
		}
	} else {
		chat = c
	}
	garden := gardener.New(db, mgr, embedder, chat, rec, gardener.FromConfig(cfg.Gardener), logger)

	if cfg.MCP.APIKey == "" {
		slog.Warn("mcp.api_key is empty; MCP and hook requests will be rejected -- set it in seamless.yaml")
	}
	mcpSrv := mcp.New(mcp.Config{
		DB: db, Files: mgr, Retrieve: ret, Events: rec, Gardener: garden, Embedder: embedder,
		APIKey: cfg.MCP.APIKey, Version: buildVersion(),
		ToolEventMaxChars:   cfg.Budgets.ToolEventMaxChars,
		CaptureAllowedPorts: cfg.Capture.AllowedPorts, Logger: logger,
	})
	hooksH := hooks.NewHandler(hooks.Config{
		DB: db, Retrieve: ret, Events: rec, Files: mgr,
		APIKey: cfg.MCP.APIKey, MaxEventChars: cfg.Budgets.ToolEventMaxChars,
		PlanCapture: cfg.PlanCapture, Logger: logger,
	})
	consoleSrv, err := console.New(console.Config{
		DB: db, Files: mgr, Gardener: garden, Events: rec, Retrieve: ret,
		APIKey: cfg.MCP.APIKey, DataDir: cfg.DataDir, ConfigPath: absConfigPath(cfg.SourcePath()),
		Budgets: cfg.Budgets, GardenerCfg: cfg.Gardener, BriefingCfg: cfg.Briefing,
		SessionIdleTTL: time.Duration(cfg.Gardener.SessionIdleMinutes) * time.Minute,
		Logger:         logger,
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
	// Redirect the bare root to the console; {$} matches only "/" so other
	// unmatched paths still return 404.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/console/", http.StatusFound)
	})
	mux.HandleFunc("/healthz", healthzHandler(db))
	mux.Handle("/api/mcp", mcpSrv.Handler())
	hooksH.Register(mux)
	consoleSrv.Register(mux)

	// Host allowlist outermost, so a rebound request is refused before it can
	// reach even an unauthenticated route (see netguard.go).
	srv := newHTTPServer(ctx, bind, hostGuard(bind, mux))
	warnNonLoopbackBind(bind)

	errCh := make(chan error, 1)
	go func() {
		slog.Info("seamlessd listening", "addr", bind, "data_dir", cfg.DataDir,
			"version", buildVersion(), "commit", commit, "built", buildDate, "mcp_tools", mcp.ToolCount)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err := <-errCh:
		stop() // stop background work (gardener, watcher ctx) on the error path too
		garden.Wait()
		return fmt.Errorf("seamlessd.serve: %w", err)
	}

	// Drain order: cancel ctx (stops the gardener loop and unblocks long-lived
	// request streams via the server's base context), shut the listener down,
	// then wait for the gardener so no pass still touches the DB when the
	// deferred mgr.Close/db.Close run.
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		garden.Wait()
		return fmt.Errorf("seamlessd.serve: shutdown: %w", err)
	}
	garden.Wait()
	slog.Info("seamlessd stopped")
	return nil
}

// applyServeEnv wires the two supervisor-facing serve flags before config load
// and returns a cleanup that closes the log file (always safe to call).
//
// --config becomes config.Load's documented $SEAMLESS_CONFIG override, moved onto
// the command line so a supervisor that cannot carry an env prefix -- notably a
// Windows Scheduled Task -- can still pin the config explicitly. Exporting it keeps
// config.Load's search order the single code path, the same shape as `seam hook
// --config`.
//
// --log-file gives the daemon a log the supervisor does not capture for it:
// launchd/systemd redirect stderr to ~/.seamless/seamlessd.log, a Scheduled Task
// does not, so the task passes --log-file to reach the same file every doc and the
// installer's health-check message point at. stderr stays wired, so an interactive
// `serve` still prints and a foreground run tees to the file.
func applyServeEnv(cfgPath, logFile string) (func(), error) {
	noop := func() {}
	if cfgPath != "" {
		if err := os.Setenv("SEAMLESS_CONFIG", cfgPath); err != nil {
			return noop, fmt.Errorf("seamlessd.serve: set config path: %w", err)
		}
	}
	if logFile == "" {
		return noop, nil
	}
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return noop, fmt.Errorf("seamlessd.serve: open log file: %w", err)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, f),
		&slog.HandlerOptions{Level: slog.LevelInfo})))
	return func() { _ = f.Close() }, nil
}

// newHTTPServer builds the daemon's HTTP server. Requests inherit ctx as their
// base context, so cancelling it (the shutdown signal) also cancels in-flight
// request contexts -- without this, the console's open SSE streams never end and
// every Shutdown stalls for its full deadline, then fails.
//
// The timeouts are chosen around the one long-lived response this server has,
// the console's SSE stream (audit L7):
//
//   - ReadHeaderTimeout / ReadTimeout bound the request side. ReadTimeout is
//     safe here only because it is *per request* and the SSE handler explicitly
//     clears its own read deadline (see console.sse); left in place it would
//     expire mid-stream, and Go's background read would then cancel the request
//     context and kill the feed.
//   - WriteTimeout stays omitted for the same reason -- it has no per-handler
//     escape hatch, so any value at all would cap how long a feed may stay open.
//   - IdleTimeout bounds keep-alive connections between requests, which is the
//     half ReadHeaderTimeout never covered: without it a client can hold open
//     connections (and their goroutines) indefinitely by simply saying nothing.
func newHTTPServer(ctx context.Context, bind string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              bind,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB; Go's default, made explicit
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
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
		if err := json.NewEncoder(w).Encode(map[string]string{
			"status": status, "version": buildVersion(), "commit": commit, "built": buildDate,
		}); err != nil {
			slog.Warn("healthz: encode response", "err", err)
		}
	}
}

// runDoctor runs self-checks. P0 fleshes this out step by step; later steps add
// config loading and database checks. See doctor.go.
func runDoctor(args []string) error {
	return doctor(args)
}
