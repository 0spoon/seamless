//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockOffset places the Windows lock on one byte at 1 GiB, far past the PID the
// holder writes at the start of the file. A LockFileEx lock is mandatory for the
// range it covers -- another handle cannot even READ a locked byte -- so locking
// the content would stop a refused process from naming the holder. Locking past
// end-of-file is legal, and this is the offset SQLite locks on every database
// file (its pending byte), so it is well trodden on local and SMB file systems.
const lockOffset = 1 << 30

// lockFile takes an exclusive LockFileEx lock on f without blocking.
// os.OpenFile opens a synchronous handle, so LOCKFILE_FAIL_IMMEDIATELY answers
// at once; ERROR_IO_PENDING is treated as held all the same, in case the handle
// is ever overlapped.
func lockFile(f *os.File) error {
	ol := &windows.Overlapped{Offset: lockOffset}
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return errLockHeld
	case errors.Is(err, windows.ERROR_NOT_SUPPORTED), errors.Is(err, windows.ERROR_INVALID_FUNCTION):
		return fmt.Errorf("%w: LockFileEx: %w", errLockUnsupported, err)
	default:
		return fmt.Errorf("LockFileEx: %w", err)
	}
}

// unlockFile releases f's lock on the same byte lockFile took.
func unlockFile(f *os.File) error {
	ol := &windows.Overlapped{Offset: lockOffset}
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol); err != nil {
		return fmt.Errorf("UnlockFileEx: %w", err)
	}
	return nil
}
