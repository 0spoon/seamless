package hooks

// Reading the current commit straight out of .git, with no exec dependency --
// the stamp on a captured plan or subagent note records the tree it was written
// against. Every failure yields "": the stamp is best-effort by design.

import (
	"os"
	"path/filepath"
	"strings"
)

// gitHead resolves the repo's current commit at cwd by reading .git directly
// (no exec dependency): a detached HEAD is the hash itself; a symbolic ref is
// dereferenced via its loose ref file, then packed-refs, following a
// worktree's gitdir pointer and commondir. Any failure yields "" -- the stamp
// is best-effort by design.
func gitHead(cwd string) string {
	if cwd == "" {
		return ""
	}
	gitDir := filepath.Join(cwd, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		// A worktree/submodule .git file: "gitdir: <path>".
		b, err := os.ReadFile(gitDir)
		if err != nil {
			return ""
		}
		after, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
		if !ok {
			return ""
		}
		gitDir = strings.TrimSpace(after)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(cwd, gitDir)
		}
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(head))
	after, ok := strings.CutPrefix(ref, "ref:")
	if !ok {
		return ref // detached HEAD: the hash itself
	}
	refName := strings.TrimSpace(after)
	if refName == "" || strings.Contains(refName, "..") {
		return ""
	}
	// Loose ref in the git dir, then its commondir (worktrees), then packed-refs.
	dirs := []string{gitDir}
	if b, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common := strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		dirs = append(dirs, filepath.Clean(common))
	}
	for _, d := range dirs {
		if b, err := os.ReadFile(filepath.Join(d, filepath.FromSlash(refName))); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	for _, d := range dirs {
		if hash := packedRef(filepath.Join(d, "packed-refs"), refName); hash != "" {
			return hash
		}
	}
	return ""
}

// packedRef scans a packed-refs file for refName and returns its hash, or "".
func packedRef(path, refName string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(b)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		if hash, name, ok := strings.Cut(line, " "); ok && name == refName {
			return hash
		}
	}
	return ""
}
