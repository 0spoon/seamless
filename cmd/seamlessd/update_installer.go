package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/arctop/seamless/internal/update"
)

const (
	// installerTimeout bounds one installer run. A normal run takes seconds to
	// a minute (a download, a service restart, a 30s health wait); fifteen
	// minutes is a hung download or a hung service manager.
	installerTimeout = 15 * time.Minute
	// installerWaitDelay bounds how long a finished installer's output pipe is
	// waited on when a process it started still holds it open.
	installerWaitDelay = 10 * time.Second
)

// errInstallerTimeout is an installer stopped for running past its timeout.
var errInstallerTimeout = errors.New("the installer did not finish in time")

// installerRun is one run of a release's install script.
type installerRun struct {
	script []byte
	// env is the installer's whole environment (unattendedInstallerEnv or
	// attendedInstallerEnv); never nil, which exec would read as "inherit".
	env []string
	// out receives the installer's stdout and stderr.
	out io.Writer
	// unattended runs it detached from any terminal (installerProcAttr).
	unattended bool
	timeout    time.Duration
}

// runInstallerScript runs a release's install script the way the documented
// one-liners do -- the interpreter reading the script from stdin -- with the
// interpreter named by absolute path (installerInterpreter). It returns nil
// for a zero exit, the exit error otherwise, and errInstallerTimeout when it
// had to stop the installer. It deliberately uses no context: a context that
// ends with the daemon's would kill the installer at the very moment the
// installer restarts the daemon.
func runInstallerScript(run installerRun) error {
	prog, args, err := installerInterpreter()
	if err != nil {
		return err
	}
	cmd := exec.Command(prog, args...)
	cmd.Stdin = bytes.NewReader(run.script)
	cmd.Stdout = run.out
	cmd.Stderr = run.out
	cmd.Env = run.env
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	cmd.SysProcAttr = installerProcAttr(run.unattended)
	cmd.WaitDelay = installerWaitDelay
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", prog, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(run.timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		kerr := stopInstaller(cmd.Process, run.unattended)
		<-done
		return errors.Join(fmt.Errorf("%w: stopped after %s", errInstallerTimeout, run.timeout), kerr)
	}
}

// updateLogKeep is how many attempt logs stay in <data_dir>/update/logs.
const updateLogKeep = 20

// updateLogDir holds one log per recorded attempt.
func updateLogDir(dataDir string) string { return filepath.Join(update.StateDir(dataDir), "logs") }

// openUpdateLog creates the attempt's log, <data_dir>/update/logs/<id>.log
// (0600 in a 0700 directory): the updater's own steps and every installer
// run's output, in order. One file per attempt, so each record's LogPath
// names its own run for good; the oldest beyond updateLogKeep are removed.
func openUpdateLog(dataDir, attemptID string) (*os.File, string, error) {
	dir := updateLogDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, attemptID+".log")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, "", err
	}
	pruneUpdateLogs(dir, updateLogKeep)
	return f, path, nil
}

// pruneUpdateLogs removes all but the newest keep attempt logs. Attempt ids
// are ULIDs, which sort by creation time; only <ULID>.log names are touched.
// Best effort: a log that will not go is only space.
func pruneUpdateLogs(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var logs []string
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".log")
		if ok && e.Type().IsRegular() && update.ValidAttemptID(id) {
			logs = append(logs, e.Name())
		}
	}
	slices.Sort(logs)
	for _, name := range logs[:max(0, len(logs)-keep)] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}
