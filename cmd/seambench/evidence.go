// Step evidence: the untracked world-state files a scenario step points its
// agent at (an incident log, a pasted report). Evidence is materialized into
// the arm's demo repo just before the step and removed unconditionally after
// it -- BEFORE any diff or capture -- because gitDiff stages untracked files,
// so evidence left behind would read as the agent's own work on every arm and
// leak the scenario's answer into the preserved artifacts.
//
// Evidence is world-state, never Seamless scaffolding: it must not breach the
// demo repo's Seamless-free invariant (memory
// scene-demo-repo-must-be-seamless-free) any more than a support ticket pasted
// into a prompt would.

package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// writeEvidence materializes a step's evidence files under repo and returns
// the remover that takes them back out. Paths must be repo-local and must not
// already exist -- colliding with a fixture file would turn scaffolding into a
// graded modification. A nil/empty map is a no-op with a no-op remover.
func writeEvidence(repo string, files map[string]string) (func() error, error) {
	if len(files) == 0 {
		return func() error { return nil }, nil
	}
	var written []string
	var madeDirs []string
	remover := func() error { return removeEvidence(written, madeDirs) }

	for _, rel := range slices.Sorted(maps.Keys(files)) {
		if !filepath.IsLocal(rel) {
			return remover, fmt.Errorf("evidence path %q escapes the repo", rel)
		}
		path := filepath.Join(repo, filepath.FromSlash(rel))
		if _, err := os.Lstat(path); err == nil {
			return remover, fmt.Errorf("evidence path %q already exists in the repo", rel)
		} else if !os.IsNotExist(err) {
			return remover, fmt.Errorf("stat evidence path %q: %w", rel, err)
		}
		dirs, err := mkdirsFor(repo, filepath.Dir(path))
		if err != nil {
			return remover, err
		}
		madeDirs = append(madeDirs, dirs...)
		if err := os.WriteFile(path, []byte(files[rel]), 0o644); err != nil {
			return remover, fmt.Errorf("write evidence %q: %w", rel, err)
		}
		written = append(written, path)
	}
	return remover, nil
}

// mkdirsFor creates dir and any missing parents inside repo, reporting the
// directories it actually created (deepest last) so removal can prune exactly
// those.
func mkdirsFor(repo, dir string) ([]string, error) {
	var missing []string
	for d := dir; strings.HasPrefix(d, repo) && d != repo; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat evidence dir %s: %w", d, err)
		}
		missing = append(missing, d)
	}
	slices.Reverse(missing) // parents first
	for _, d := range missing {
		if err := os.Mkdir(d, 0o755); err != nil {
			return missing, fmt.Errorf("create evidence dir %s: %w", d, err)
		}
	}
	return missing, nil
}

// removeEvidence deletes the written files and prunes the directories the
// write created. A file the agent itself deleted is not an error; a directory
// the agent put its own files into is left alone (os.Remove refuses a
// non-empty dir, and that refusal is deliberate -- the agent's work belongs to
// the diff).
func removeEvidence(files, dirs []string) error {
	var firstErr error
	for _, f := range files {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = fmt.Errorf("remove evidence %s: %w", f, err)
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- { // deepest first
		if err := os.Remove(dirs[i]); err != nil && !os.IsNotExist(err) {
			if isNotEmpty(err) {
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("prune evidence dir %s: %w", dirs[i], err)
			}
		}
	}
	return firstErr
}

// isNotEmpty reports a directory-not-empty removal error, which removal
// tolerates: the agent wrote its own files there and they are its work.
func isNotEmpty(err error) bool {
	return strings.Contains(err.Error(), "not empty")
}
