//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes flock(2)'s exclusive lock on f without blocking. flock rather
// than fcntl: an fcntl lock belongs to the whole process and is dropped when ANY
// descriptor for the file closes, even one unrelated code opened on the same
// path, while a flock lock belongs to this open file description and lives
// exactly as long as it. Go opens files close-on-exec, so a child process never
// inherits it either.
func lockFile(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EWOULDBLOCK):
			return errLockHeld
		case errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EOPNOTSUPP), errors.Is(err, unix.ENOLCK):
			return fmt.Errorf("%w: flock: %w", errLockUnsupported, err)
		default:
			return fmt.Errorf("flock: %w", err)
		}
	}
}

// unlockFile releases f's flock lock.
func unlockFile(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
		return fmt.Errorf("flock unlock: %w", err)
	}
	return nil
}
