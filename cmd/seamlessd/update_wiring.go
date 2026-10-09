package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"runtime"

	"github.com/arctop/seamless/internal/config"
	"github.com/arctop/seamless/internal/store"
	"github.com/arctop/seamless/internal/update"
)

// updateCheckerDeps assembles the background update check's dependencies for
// this daemon: the build stamp, how it was installed (update.Detect over the
// probe in install_probe.go), the file/env update config with the console's
// override read live from the settings table, the GitHub fetcher and the
// event log. It is split from runServe so a test can build the same checker
// over a fake fetcher and probe.
func updateCheckerDeps(cfg config.Config, db *sql.DB, rec update.Recorder, instance string, install update.Install, logger *slog.Logger) update.Deps {
	return update.Deps{
		DataDir:      cfg.DataDir,
		Version:      version,
		Distribution: distribution,
		GOOS:         runtime.GOOS,
		Install:      install,
		Instance:     instance,
		PID:          os.Getpid(),
		LocalHost:    config.Hostname(),
		Base:         cfg.Update,
		Override: func(ctx context.Context) (config.UpdateOverride, error) {
			o, _, err := store.UpdateOverride(ctx, db)
			return o, err
		},
		Fetcher: update.NewFetcher(),
		Events:  rec,
		Log:     logger,
	}
}

// newUpdateChecker builds the daemon's update checker. Nothing runs until
// Start, which runServe calls only after the listener is bound.
func newUpdateChecker(cfg config.Config, db *sql.DB, rec update.Recorder, instance string, logger *slog.Logger) *update.Checker {
	install := update.Detect(probeInstall(distribution, version, absConfigPath(cfg.SourcePath()), cfg.IsClient()))
	chk := update.New(updateCheckerDeps(cfg, db, rec, instance, install, logger))
	mode, reason := chk.Status().Mode()
	slog.Info("update check", "mode", string(mode), "install", string(install.Kind), "reason", reason,
		"distribution", distribution)
	return chk
}

// healthzBody is the /healthz JSON: liveness, the build, and -- with an
// update check wired -- this process's instance id and the newest release
// when the check found one newer than this build. The instance lets a caller
// tell this daemon from another answering on the same port (another user's
// on a shared Windows box), and from the daemon it replaced.
func healthzBody(status string, instance string, st *update.Status) map[string]string {
	body := map[string]string{
		"status": status, "version": buildVersion(), "commit": commit, "built": buildDate,
		"distribution": distribution,
	}
	if instance != "" {
		body["instance"] = instance
	}
	if st != nil && st.Settings.Check && st.Available && st.Newest != nil {
		body["update_available"] = st.Newest.Version.String()
	}
	return body
}
