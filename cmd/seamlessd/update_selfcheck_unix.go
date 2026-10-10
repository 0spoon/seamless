//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// ownProcess reads this process's id and process group for
// outsideServiceCheck.
func ownProcess() (selfProcess, error) {
	pgid, err := unix.Getpgid(0)
	if err != nil {
		return selfProcess{}, fmt.Errorf("read the process group: %w", err)
	}
	return selfProcess{pid: os.Getpid(), pgid: pgid}, nil
}

// errWindowsOnly is a Windows-only fact asked for on Unix, where
// probeSupervised never asks for it.
var errWindowsOnly = errors.New("only Windows has this")

// parentImageName is Windows' half of gate 9.
func parentImageName() (string, error) { return "", errWindowsOnly }

// isCurrentUser is Windows' half of gate 9.
func isCurrentUser(string) (bool, error) { return false, errWindowsOnly }
