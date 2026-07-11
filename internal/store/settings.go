package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/0spoon/seamless/internal/core"
)

// SettingRepoProjectMap is the settings key holding the cwd->project-slug map: a
// JSON object {absolute-path: slug}. The hooks and session_start resolve an
// agent's working directory to a project slug through it.
const SettingRepoProjectMap = "repo_project_map"

// SettingProjectFamilies is the settings key holding project families: a JSON
// object {family-name: [slug, ...]}. Sibling briefings surface recent findings
// from a project's family members.
const SettingProjectFamilies = "project_families"

// ProjectFamilies decodes the project_families setting into a map. An unset or
// blank value yields an empty map (not an error).
func ProjectFamilies(ctx context.Context, db *sql.DB) (map[string][]string, error) {
	raw, found, err := GetSetting(ctx, db, SettingProjectFamilies)
	if err != nil {
		return nil, err
	}
	if !found || strings.TrimSpace(raw) == "" {
		return map[string][]string{}, nil
	}
	var m map[string][]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("store.ProjectFamilies: decode: %w", err)
	}
	return m, nil
}

// SiblingProjects returns the other project slugs sharing a family with project,
// deduped and excluding project itself. A project may appear in more than one
// family; all such siblings are unioned. Returns nil for the global scope or a
// project with no family.
func SiblingProjects(ctx context.Context, db *sql.DB, project string) ([]string, error) {
	if project == "" {
		return nil, nil
	}
	families, err := ProjectFamilies(ctx, db)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{project: true}
	var out []string
	for _, members := range families {
		if !slices.Contains(members, project) {
			continue
		}
		for _, slug := range members {
			if slug == "" || seen[slug] {
				continue
			}
			seen[slug] = true
			out = append(out, slug)
		}
	}
	return out, nil
}

// GetSetting returns the value for a settings key. found is false when unset.
func GetSetting(ctx context.Context, db *sql.DB, key string) (string, bool, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store.GetSetting: %w", err)
	}
	return v, true, nil
}

// SetSetting upserts a settings key/value.
func SetSetting(ctx context.Context, db *sql.DB, key, value string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store.SetSetting: %w", err)
	}
	return nil
}

// RepoProjectMap decodes the repo_project_map setting into a map. An unset or
// blank value yields an empty map (not an error), so an unconfigured install
// simply resolves every cwd to the global scope.
func RepoProjectMap(ctx context.Context, db *sql.DB) (map[string]string, error) {
	raw, found, err := GetSetting(ctx, db, SettingRepoProjectMap)
	if err != nil {
		return nil, err
	}
	if !found || strings.TrimSpace(raw) == "" {
		return map[string]string{}, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("store.RepoProjectMap: decode: %w", err)
	}
	return m, nil
}

// ResolveProjectForCWD maps an absolute working directory to a project slug via
// the repo_project_map, using a longest-prefix match. It is read-only and
// failure-soft: an unresolvable cwd (or an unconfigured map) returns "" (the
// global scope), never an error from the matching itself. Use it on read paths
// (briefing, prompt recall); use RegisterProjectForCWD on session-start paths
// that should grow the map.
func ResolveProjectForCWD(ctx context.Context, db *sql.DB, cwd string) (string, error) {
	m, err := RepoProjectMap(ctx, db)
	if err != nil {
		return "", err
	}
	return matchProjectPath(cwd, m), nil
}

// AddRepoMapping records repoPath -> slug in the repo_project_map and persists
// it, so the mapping survives restarts and is shared by every agent. It is a
// no-op when that exact entry already exists, so callers may invoke it on every
// resolve without churning the setting.
func AddRepoMapping(ctx context.Context, db *sql.DB, repoPath, slug string) error {
	repoPath = filepath.Clean(repoPath)
	m, err := RepoProjectMap(ctx, db)
	if err != nil {
		return err
	}
	if m[repoPath] == slug {
		return nil
	}
	m[repoPath] = slug
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("store.AddRepoMapping: %w", err)
	}
	return SetSetting(ctx, db, SettingRepoProjectMap, string(b))
}

// RegisterProjectForCWD resolves cwd to a project slug like ResolveProjectForCWD,
// but grows the map at runtime when cwd falls outside every mapped repo: it finds
// the enclosing git repository, derives a slug from that repo's directory name,
// records repoRoot -> slug in the repo_project_map, and ensures a projects-table
// row. This is how the repo->project map evolves as agents work in new repos --
// no recompile, no manual map-repo. For an already-mapped cwd it still backfills
// the registry row, so project_list stays complete. A blank cwd, or a cwd outside
// any git repo, resolves to the global scope ("") and registers nothing.
func RegisterProjectForCWD(ctx context.Context, db *sql.DB, cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		return "", nil
	}
	m, err := RepoProjectMap(ctx, db)
	if err != nil {
		return "", err
	}
	if slug := matchProjectPath(cwd, m); slug != "" {
		// Already mapped: backfill the registry row (idempotent) and return.
		if _, err := EnsureProject(ctx, db, slug, slug); err != nil {
			return "", err
		}
		return slug, nil
	}

	root := gitRepoRoot(cwd)
	if root == "" {
		return "", nil // not inside a git repo: stay global, register nothing
	}
	name := filepath.Base(root)
	slug := uniqueProjectSlug(core.Slugify(name), root, m)
	if _, err := EnsureProject(ctx, db, slug, name); err != nil {
		return "", err
	}
	if err := AddRepoMapping(ctx, db, root, slug); err != nil {
		return "", err
	}
	return slug, nil
}

// gitRepoRoot returns the nearest ancestor of dir (inclusive) containing a .git
// entry -- the repository root -- or "" if dir is not inside a git repo. A .git
// file (worktrees, submodules) counts as well as a .git directory.
func gitRepoRoot(dir string) string {
	dir = filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached the filesystem root without finding .git
		}
		dir = parent
	}
}

// uniqueProjectSlug returns base unless another repo path already owns it in m,
// in which case it appends -2, -3, ... until free. The guard keeps a newly seen
// repo whose directory name collides with an existing project (e.g. two distinct
// "backend" repos) from silently inheriting that project's memories; the owner
// can still merge them later with map-repo.
func uniqueProjectSlug(base, root string, m map[string]string) string {
	root = filepath.Clean(root)
	takenByOther := func(slug string) bool {
		for path, s := range m {
			if s == slug && filepath.Clean(path) != root {
				return true
			}
		}
		return false
	}
	if !takenByOther(base) {
		return base
	}
	for i := 2; ; i++ {
		if cand := base + "-" + strconv.Itoa(i); !takenByOther(cand) {
			return cand
		}
	}
}

// matchProjectPath returns the slug of the longest map key that is a path-prefix
// of cwd, or "" when none matches. Prefixes must align on a path separator, so
// "/a/foo" never matches key "/a/foobar".
func matchProjectPath(cwd string, m map[string]string) string {
	if cwd == "" || len(m) == 0 {
		return ""
	}
	cwd = filepath.Clean(cwd)
	best, bestSlug := "", ""
	for prefix, slug := range m {
		p := filepath.Clean(prefix)
		if pathHasPrefix(cwd, p) && len(p) > len(best) {
			best, bestSlug = p, slug
		}
	}
	return bestSlug
}

// pathHasPrefix reports whether path is prefix itself or lies beneath it, with
// the match aligned on a separator boundary.
func pathHasPrefix(path, prefix string) bool {
	if path == prefix {
		return true
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := path[len(prefix):]
	return strings.HasPrefix(rest, string(filepath.Separator))
}
