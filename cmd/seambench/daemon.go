// The arm's Seamless daemon, up for the duration of one run.
//
// The harness only stands an instance up briefly to self-check the briefing;
// an actual run needs it live the whole time, because both halves of the
// mechanism talk to it: the agent's SessionStart/UserPromptSubmit hooks POST
// /api/hooks/*, and its MCP client talks to /api/mcp. A vanilla arm has no
// daemon at all.
//
// The daemon is stopped -- and waited for -- before anything reads the arm's
// data dir, so the event dump and the preserved data/ copy see a cleanly closed
// single-writer SQLite database rather than a hot WAL.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync/atomic"
	"time"
)

// healthTimeout bounds how long a fresh daemon may take to answer /healthz.
const healthTimeout = 30 * time.Second

// healthPoll is the interval between /healthz probes.
const healthPoll = 100 * time.Millisecond

// stopFunc shuts the arm's daemon down and waits for it to exit.
type stopFunc func()

// serveFunc starts the arm's daemon and returns once it answers /healthz. It is
// a field on the runner so tests can substitute a stub for the one part of a
// run that needs a built binary and a real port.
type serveFunc func(ctx context.Context, a *arm, logPath string) (stopFunc, error)

// execServe runs the given seamlessd binary as the arm's daemon.
func execServe(binary string) serveFunc {
	return func(ctx context.Context, a *arm, logPath string) (stopFunc, error) {
		logFile, err := os.Create(logPath)
		if err != nil {
			return nil, fmt.Errorf("create daemon log %s: %w", logPath, err)
		}

		// Cancel interrupts rather than kills, so the daemon closes its DB;
		// WaitDelay is the backstop that turns a hung shutdown into a kill
		// instead of a stuck benchmark.
		runCtx, cancel := context.WithCancel(ctx)
		cmd := exec.CommandContext(runCtx, binary, "serve")
		cmd.Dir = a.dir
		cmd.Env = a.daemonEnv()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
		cmd.WaitDelay = 5 * time.Second

		if err := cmd.Start(); err != nil {
			cancel()
			_ = logFile.Close()
			return nil, fmt.Errorf("start seamlessd for arm %s: %w", a.condition.Name, err)
		}

		var stopping atomic.Bool
		waited := make(chan error, 1)
		go func() {
			err := cmd.Wait()
			if !stopping.Load() {
				// The daemon died mid-run: the arm's hooks and MCP calls after
				// this point silently did nothing, which would otherwise look
				// like an agent that simply ignored Seamless.
				slog.Warn("arm daemon exited before the run finished",
					"condition", a.condition.Name, "log", logPath, "err", err)
			}
			waited <- err
		}()

		stop := func() {
			stopping.Store(true)
			cancel()
			<-waited
			_ = logFile.Close()
		}

		if err := waitHealthy(runCtx, a.url); err != nil {
			stop()
			return nil, fmt.Errorf("arm %s daemon never became healthy (see %s): %w", a.condition.Name, logPath, err)
		}
		return stop, nil
	}
}

// drainClose empties and closes a probe response so the polling client can
// reuse the connection.
func drainClose(body io.ReadCloser) {
	//nolint:errcheck // the body is being discarded; a read failure changes nothing about the probe's verdict.
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}

// waitHealthy polls the daemon's /healthz until it answers 200 or the deadline
// passes.
func waitHealthy(ctx context.Context, baseURL string) error {
	deadline, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(healthPoll)
	defer ticker.Stop()

	var lastErr error
	for {
		req, err := http.NewRequestWithContext(deadline, http.MethodGet, baseURL+"/healthz", nil)
		if err != nil {
			return fmt.Errorf("build health request: %w", err)
		}
		resp, err := client.Do(req)
		switch {
		case err != nil:
			lastErr = err
		case resp.StatusCode == http.StatusOK:
			drainClose(resp.Body)
			return nil
		default:
			lastErr = fmt.Errorf("healthz returned %s", resp.Status)
			drainClose(resp.Body)
		}
		select {
		case <-deadline.Done():
			// deadline.Err() distinguishes "the daemon took longer than
			// healthTimeout" from "the whole run was cancelled".
			return errors.Join(fmt.Errorf("%s/healthz not ready: %w", baseURL, deadline.Err()), lastErr)
		case <-ticker.C:
		}
	}
}
