//go:build darwin

package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/arctop/seamless/internal/update"
)

// osUpdateSpawner is darwin's spawner: the updater as a child in a session of
// its own (startDetached).
func osUpdateSpawner(t spawnTarget, _ *slog.Logger) update.Spawner {
	return setsidSpawner{target: t}
}

// setsidSpawner starts the updater with Setsid. launchd stops the service by
// signalling the job's process group, on bootout, kickstart -k, a kill of the
// main process and the exit timeout alike, and never escalates to the rest of
// its session or coalition, so a session leader survives all of them (2.01,
// macOS 27). The daemon reaps it if it exits first; otherwise launchd does.
// No transient launchd job as a fallback: it would run the same binary and
// fail exactly when this does, while adding a plist, a leftover label to boot
// out, and an uninstall step.
type setsidSpawner struct {
	target spawnTarget
}

// Spawn starts req's updater. nil means the process exists.
func (s setsidSpawner) Spawn(_ context.Context, req update.SpawnRequest) error {
	if err := req.Validate(); err != nil {
		return fmt.Errorf("seamlessd.spawn: %w", err)
	}
	if err := startDetached(s.target.exe, s.target.args(req)); err != nil {
		return fmt.Errorf("seamlessd.spawn: start the updater in a session of its own: %w", err)
	}
	return nil
}
