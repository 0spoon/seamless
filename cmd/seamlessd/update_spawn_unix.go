//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// handoffProcAttr is a hand-off command's process attributes: none on Unix.
func handoffProcAttr() *syscall.SysProcAttr { return nil }

// startDetached starts exe with args in a session of its own (Setsid) -- out
// of the daemon's process group and session, so a signal to either never
// reaches it -- and reaps it in the background. Its stdin and stdout are
// /dev/null, and so is its stderr unless the daemon's is a regular file:
// launchd's log, which then also catches an updater that refuses before its
// attempt log opens. Never a pipe: once the daemon is gone nothing would drain
// it, and the updater's next write would kill it with SIGPIPE.
func startDetached(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if f := regularFile(os.Stderr); f != nil {
		cmd.Stderr = f // only a non-nil *os.File: a typed nil would close the child's fd 2
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait() //nolint:errcheck // the updater reports through its attempt record; this only reaps it
	}()
	return nil
}

// regularFile returns f when it is an open regular file, else nil.
func regularFile(f *os.File) *os.File {
	if f == nil {
		return nil
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	return f
}

// ownUpdateTask names this user's Windows update task, which Unix has none of.
func ownUpdateTask() string { return "" }
