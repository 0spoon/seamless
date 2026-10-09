//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// installerInterpreter runs a release's install script from stdin: /bin/sh by
// absolute path, never a PATH lookup, so the updater runs the shell the
// installer was written for whatever PATH it inherited.
func installerInterpreter() (string, []string, error) {
	return "/bin/sh", []string{"-s"}, nil
}

// installerProcAttr puts an unattended installer in a process group of its
// own, so a timeout can stop the installer together with whatever it is
// waiting on (curl, tar, a hung systemctl). An attended one stays in the
// terminal's foreground group: the installer prompts on /dev/tty, and a
// process outside that group that reads it is stopped by SIGTTIN.
func installerProcAttr(unattended bool) *syscall.SysProcAttr {
	if !unattended {
		return nil
	}
	return &syscall.SysProcAttr{Setpgid: true}
}

// stopInstaller kills a running installer: its whole process group when it
// has one of its own (installerProcAttr), else the shell alone.
func stopInstaller(p *os.Process, unattended bool) error {
	if unattended {
		if err := unix.Kill(-p.Pid, unix.SIGKILL); err != nil {
			return fmt.Errorf("kill installer process group %d: %w", p.Pid, err)
		}
		return nil
	}
	return p.Kill()
}

// freeBytes is the space on dir's file system available to this user.
func freeBytes(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	// Both conversions are needed somewhere: the field types differ by OS.
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
