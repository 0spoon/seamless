package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/0spoon/seamless/internal/core"
)

// AtomicWrite writes data to path atomically: it writes a temp file in the same
// directory, fsyncs it, then renames over the target. This prevents a crash
// mid-write from corrupting a source-of-truth markdown file. Ported from Seam v1
// (note.AtomicWriteFile).
func AtomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".seamless-tmp-*")
	if err != nil {
		return fmt.Errorf("files.AtomicWrite: create temp: %w", err)
	}
	tmpPath := tmp.Name()

	cleanup := func(cause error, verb string) error {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("files.AtomicWrite: %s: %w", verb, cause)
	}

	if _, err := tmp.Write(data); err != nil {
		return cleanup(err, "write")
	}
	if err := tmp.Chmod(perm); err != nil {
		return cleanup(err, "chmod")
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(err, "sync")
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("files.AtomicWrite: close: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("files.AtomicWrite: rename: %w", err)
	}
	return nil
}

// atomicWriteRoot is AtomicWrite's corpus-specific rooted variant. The caller
// passes a root already opened on the checked real parent directory, so a
// concurrent parent rename or symlink swap cannot redirect the write outside
// it. The final entry is Lstat-checked again immediately before rename.
func atomicWriteRoot(root *os.Root, name string, data []byte, perm os.FileMode) (retErr error) {
	id, err := core.NewID()
	if err != nil {
		return fmt.Errorf("files.atomicWriteRoot: temp id: %w", err)
	}
	tmpName := ".seamless-tmp-" + id
	tmp, err := root.OpenFile(tmpName, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("files.atomicWriteRoot: create temp: %w", err)
	}
	removeTemp := true
	tempClosed := false
	defer func() {
		if !tempClosed {
			if err := tmp.Close(); err != nil && retErr == nil {
				retErr = fmt.Errorf("files.atomicWriteRoot: close temp: %w", err)
			}
		}
		if removeTemp {
			if err := root.Remove(tmpName); err != nil && !errors.Is(err, os.ErrNotExist) && retErr == nil {
				retErr = fmt.Errorf("files.atomicWriteRoot: remove temp: %w", err)
			}
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("files.atomicWriteRoot: write: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("files.atomicWriteRoot: chmod: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("files.atomicWriteRoot: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		tempClosed = true
		return fmt.Errorf("files.atomicWriteRoot: close: %w", err)
	}
	tempClosed = true

	if info, err := root.Lstat(name); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("files.atomicWriteRoot: %w: %s", ErrSymlink, name)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("files.atomicWriteRoot: %w: %s", ErrNotRegular, name)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("files.atomicWriteRoot: lstat target: %w", err)
	}
	if err := root.Rename(tmpName, name); err != nil {
		return fmt.Errorf("files.atomicWriteRoot: rename: %w", err)
	}
	removeTemp = false
	return nil
}
