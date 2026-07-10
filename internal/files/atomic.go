package files

import (
	"fmt"
	"os"
	"path/filepath"
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
