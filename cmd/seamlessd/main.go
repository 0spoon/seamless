// Command seamlessd is the Seamless server daemon and operator CLI.
//
// Subcommands (grown phase by phase per docs/PLAN.md):
//
//	seamlessd serve     start the HTTP server
//	seamlessd doctor    run configuration + database self-checks
//	seamlessd version   print the version
package main

import (
	"context"
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
  seamlessd serve     start the HTTP server (127.0.0.1:8081)
  seamlessd doctor    run configuration + database self-checks
  seamlessd version   print the version
`, version)
}

// runServe starts the HTTP server and blocks until SIGINT/SIGTERM, then shuts
// down gracefully. The routing surface (hooks, MCP, console) is added in later
// phases; P0 exposes only /healthz.
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

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)

	srv := &http.Server{
		Addr:              bind,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("seamlessd listening", "addr", bind, "data_dir", cfg.DataDir, "version", version)
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

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version}); err != nil {
		slog.Warn("healthz: encode response", "err", err)
	}
}

// runDoctor runs self-checks. P0 fleshes this out step by step; later steps add
// config loading and database checks. See doctor.go.
func runDoctor(args []string) error {
	return doctor(args)
}
