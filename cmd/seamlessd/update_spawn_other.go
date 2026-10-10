//go:build unix && !darwin && !linux

package main

import (
	"log/slog"

	"github.com/arctop/seamless/internal/update"
)

// osUpdateSpawner is nil here: the installers run no service on this OS, so
// nothing would restart a daemon, and it is only ever told about updates.
func osUpdateSpawner(spawnTarget, *slog.Logger) update.Spawner { return nil }
