package gitread

import (
	"os"
	"path/filepath"
	"strings"
)

// RepoRoot returns the nearest ancestor of dir (inclusive) containing a .git
// entry -- the repository root -- or "" if dir is not inside a git repo. A .git
// file (worktrees, submodules) counts as well as a .git directory.
func RepoRoot(dir string) string {
	dir = filepath.Clean(dir)
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached the filesystem root without finding .git
		}
		dir = parent
	}
}

// MainWorktreeRoot resolves a linked-worktree root to the root of its main
// checkout. A linked worktree's .git is a file ("gitdir: <admin dir>") whose
// admin dir lives under the main repository's .git/worktrees/<name>/, and the
// commondir file inside it points back at the shared .git directory; the main
// checkout root is that directory's parent. Everything else resolves to root
// unchanged: a regular checkout (.git directory), a submodule (gitdir under
// .git/modules/, no commondir file -- a genuinely separate repository), a bare
// main repo (commondir not named .git, so there is no main checkout), or an
// unparseable/stale layout. Pure filesystem -- hooks and session_start must not
// depend on a git executable.
func MainWorktreeRoot(root string) string {
	gitPath := filepath.Join(root, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil || info.IsDir() {
		return root
	}
	gitDir := resolveGitDir(root)
	if gitDir == "" {
		return root
	}
	common, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return root
	}
	commonDir := strings.TrimSpace(string(common))
	if commonDir == "" {
		return root
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	commonDir = filepath.Clean(commonDir)
	if filepath.Base(commonDir) != ".git" {
		return root
	}
	if info, err := os.Lstat(commonDir); err != nil || !info.IsDir() {
		return root
	}
	return filepath.Dir(commonDir)
}

// OriginURL returns the url of the repository's "origin" remote, read straight
// out of .git/config -- the one piece of repo identity that survives being
// cloned to another machine under a different path, which is what makes it
// usable for deciding whether two checkouts are the same project.
//
// "" when the repo has no origin (a purely local repo, or one whose remote goes
// by another name): absent is absent, never a guess at some other remote. The
// config is read from the worktree's own admin dir first and then the shared
// common dir, since a linked worktree keeps its remotes in the latter.
func OriginURL(root string) string {
	gitDir := resolveGitDir(root)
	if gitDir == "" {
		return ""
	}
	for _, d := range sharedDirs(gitDir) {
		if u := originFromConfig(filepath.Join(d, "config")); u != "" {
			return u
		}
	}
	return ""
}

// originFromConfig scans a git config file for the origin remote's url. Git
// config is INI-ish: a `[remote "origin"]` header opens the section, any other
// header closes it, and later assignments of a key win over earlier ones (a
// repo re-pointed with `git remote set-url` may leave both). Comments start a
// line; a `#` inside a URL is part of the URL, so trailing comments are not
// stripped.
func originFromConfig(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var url string
	inOrigin := false
	for line := range strings.Lines(string(b)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inOrigin = isOriginSection(line)
			continue
		}
		if !inOrigin {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "url") {
			continue
		}
		if v := strings.TrimSpace(value); v != "" {
			url = v
		}
	}
	return url
}

// isOriginSection reports whether an INI section header is `[remote "origin"]`.
// Git matches section names case-insensitively and subsection names (the quoted
// part) case-sensitively, so "origin" and "Origin" are different remotes.
func isOriginSection(line string) bool {
	inner := strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
	name, sub, ok := strings.Cut(inner, " ")
	if !ok || !strings.EqualFold(strings.TrimSpace(name), "remote") {
		return false
	}
	return strings.Trim(strings.TrimSpace(sub), `"`) == "origin"
}
