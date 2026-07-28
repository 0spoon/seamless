// Package gitread reads git repository state straight out of .git, with no
// exec dependency and no git library: the daemon must work on machines where a
// git binary may not exist, and hooks fire on every prompt. The readers back
// the provenance stamp on captured plans and subagent notes (internal/hooks)
// and the gardener's stale-plan ship evidence (internal/gardener). Every
// failure yields a zero value -- all reads are best-effort by design.
package gitread

import (
	"os"
	"path/filepath"
	"strings"
)

// Head resolves the repo's current commit at cwd: a detached HEAD is the hash
// itself; a symbolic ref is dereferenced via its loose ref file, then
// packed-refs, following a worktree's gitdir pointer and commondir. Any
// failure yields "".
func Head(cwd string) string {
	gitDir := resolveGitDir(cwd)
	if gitDir == "" {
		return ""
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

// ReflogEntry is one HEAD reflog line: HEAD moved from Old to New, and Message
// says why ("commit: <subject>", "merge <branch>: Fast-forward", "checkout:
// moving from a to b", ...).
type ReflogEntry struct {
	Old, New string
	Message  string
}

// ReflogHEAD returns the HEAD reflog at cwd, oldest first -- the repo's local
// record of every commit, merge, and checkout that moved HEAD. Worktrees keep
// their own HEAD log under their gitdir, so a worktree cwd reads that
// worktree's history. nil when the repo or its log cannot be read (a fresh
// clone, a pruned log): absent history is not evidence of anything.
func ReflogHEAD(cwd string) []ReflogEntry {
	gitDir := resolveGitDir(cwd)
	if gitDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(gitDir, "logs", "HEAD"))
	if err != nil {
		return nil
	}
	var entries []ReflogEntry
	for line := range strings.Lines(string(data)) {
		// <old> <new> <ident> <timestamp> <tz>\t<message>
		header, msg, _ := strings.Cut(line, "\t")
		fields := strings.Fields(header)
		if len(fields) < 2 || !isHex(fields[0]) || !isHex(fields[1]) {
			continue
		}
		entries = append(entries, ReflogEntry{
			Old: fields[0], New: fields[1], Message: strings.TrimSpace(msg),
		})
	}
	return entries
}

// resolveGitDir locates cwd's git directory, following a .git file's gitdir
// pointer (worktrees, submodules). "" when cwd is not a repository root.
func resolveGitDir(cwd string) string {
	if cwd == "" {
		return ""
	}
	gitDir := filepath.Join(cwd, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return gitDir
	}
	b, err := os.ReadFile(gitDir)
	if err != nil {
		return ""
	}
	after, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return ""
	}
	gitDir = strings.TrimSpace(after)
	if gitDir == "" {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(cwd, gitDir)
	}
	return gitDir
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

// isHex reports whether s is a plausible commit hash (all hex, length >= 40 --
// SHA-1 or SHA-256).
func isHex(s string) bool {
	if len(s) < 40 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
