// Package validate provides shared input-validation helpers enforcing the
// filesystem-safety invariants in AGENTS.md:
//   - Reject file paths containing "..", absolute paths, or null bytes.
//   - Sanitize names (memory names, project slugs, note slugs) for filesystem safety.
//
// Ported from Seam v1 (internal/validate) with one deliberate change: Title no
// longer rejects "..". Titles are slugified before any filesystem use, and the
// old check bounced legitimate titles such as "summary 2026-07-05..08" (a 37%
// error rate on create in v1). Name and Path keep the ".." guard because their
// inputs feed filesystem operations directly.
package validate

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

var (
	// ErrPathTraversal is returned when a path contains "..", is absolute, or
	// contains null bytes.
	ErrPathTraversal = fmt.Errorf("path contains traversal sequence, absolute path, or null bytes")

	// ErrUnsafeName is returned when a name contains filesystem-unsafe
	// characters or patterns.
	ErrUnsafeName = fmt.Errorf("name contains unsafe characters")
)

// Path rejects file paths that could escape a base directory. It checks for
// ".." components, absolute paths, and null bytes.
func Path(path string) error {
	if path == "" {
		return fmt.Errorf("validate.Path: empty path")
	}
	if strings.ContainsRune(path, 0) {
		return fmt.Errorf("validate.Path: %w: null byte", ErrPathTraversal)
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("validate.Path: %w: absolute path", ErrPathTraversal)
	}

	cleaned := filepath.Clean(path)
	if slices.Contains(strings.Split(cleaned, string(filepath.Separator)), "..") {
		return fmt.Errorf("validate.Path: %w: dot-dot component", ErrPathTraversal)
	}
	return nil
}

// PathWithinDir validates a path and then verifies the resolved absolute path
// stays within the given base directory.
func PathWithinDir(relPath, baseDir string) error {
	if err := Path(relPath); err != nil {
		return err
	}
	absPath := filepath.Clean(filepath.Join(baseDir, relPath))
	baseDir = filepath.Clean(baseDir)

	if !strings.HasPrefix(absPath, baseDir+string(filepath.Separator)) && absPath != baseDir {
		return fmt.Errorf("validate.PathWithinDir: %w: resolved path escapes base directory", ErrPathTraversal)
	}
	return nil
}

// Title validates human-facing titles and descriptions. It is more permissive
// than Name: it allows "/" (e.g. "TCP/IP", "A/B testing") and ".." (e.g. date
// ranges like "2026-07-05..08") because titles are slugified before any
// filesystem use. It rejects empty strings, null bytes, backslashes, and
// lengths over 255.
func Title(title string) error {
	if title == "" {
		return fmt.Errorf("validate.Title: title is empty")
	}
	if len(title) > 255 {
		return fmt.Errorf("validate.Title: %w: exceeds 255 characters", ErrUnsafeName)
	}
	if strings.ContainsRune(title, 0) {
		return fmt.Errorf("validate.Title: %w: null byte", ErrUnsafeName)
	}
	if strings.Contains(title, "\\") {
		return fmt.Errorf("validate.Title: %w: backslash", ErrUnsafeName)
	}
	return nil
}

// Name rejects names used to build filenames (memory names, project/note slugs)
// that contain filesystem-unsafe characters: null bytes, forward/back slashes,
// or ".." sequences. It also enforces a maximum length of 255 characters.
func Name(name string) error {
	if name == "" {
		return fmt.Errorf("validate.Name: name is empty")
	}
	if len(name) > 255 {
		return fmt.Errorf("validate.Name: %w: exceeds 255 characters", ErrUnsafeName)
	}
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("validate.Name: %w: null byte", ErrUnsafeName)
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("validate.Name: %w: forward slash", ErrUnsafeName)
	}
	if strings.Contains(name, "\\") {
		return fmt.Errorf("validate.Name: %w: backslash", ErrUnsafeName)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("validate.Name: %w: dot-dot sequence", ErrUnsafeName)
	}
	return nil
}
