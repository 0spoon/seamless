package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// SettingRepoProjectMap is the settings key holding the cwd->project-slug map: a
// JSON object {absolute-path: slug}. The hooks and session_start resolve an
// agent's working directory to a project slug through it.
const SettingRepoProjectMap = "repo_project_map"

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
// the repo_project_map, using a longest-prefix match. It is failure-soft: an
// unresolvable cwd (or an unconfigured map) returns "" (the global scope), never
// an error from the matching itself.
func ResolveProjectForCWD(ctx context.Context, db *sql.DB, cwd string) (string, error) {
	m, err := RepoProjectMap(ctx, db)
	if err != nil {
		return "", err
	}
	return matchProjectPath(cwd, m), nil
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
