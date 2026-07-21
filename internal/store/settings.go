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

	"github.com/0spoon/seamless/internal/config"
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

// ErrFamilyNotFound is returned by RemoveFamilyMembers when the named family does
// not exist.
var ErrFamilyNotFound = errors.New("store: project family not found")

// ErrFamilyExists is returned when a family create or rename would overwrite a
// different existing family.
var ErrFamilyExists = errors.New("store: project family already exists")

// ErrFamilyNoMembers is returned when a family replacement contains no usable
// project slugs. Empty families are not persisted.
var ErrFamilyNoMembers = errors.New("store: project family has no members")

// settingsExecutor is the read+write subset shared by *sql.DB and *sql.Tx (the
// mirror of rowQuerier in tasks.go), so a settings read-decode-mutate-write runs
// identically on the pool or inside one transaction. The family mutators need it
// to keep their read and their write-back in the same transaction.
type settingsExecutor interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ProjectFamilies decodes the project_families setting into a map. An unset or
// blank value yields an empty map (not an error).
func ProjectFamilies(ctx context.Context, db *sql.DB) (map[string][]string, error) {
	return projectFamiliesTx(ctx, db)
}

// projectFamiliesTx decodes the project_families setting via any executor, so a
// mutator can read the map inside its own transaction.
func projectFamiliesTx(ctx context.Context, q settingsExecutor) (map[string][]string, error) {
	raw, found, err := getSettingTx(ctx, q, SettingProjectFamilies)
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

// SetProjectFamilies persists the full families map as the project_families
// setting. Empty families (no members) are dropped and members are trimmed and
// deduped so the stored value stays canonical; passing an empty map clears the
// setting to "{}". It is the single writer the family CLI and mutators funnel
// through, so the setting never accumulates blanks or duplicates.
//
// It reads nothing, so unlike the read-modify-write mutators below it needs no
// transaction: last-write-wins is inherent to its "set the whole map" contract.
func SetProjectFamilies(ctx context.Context, db *sql.DB, families map[string][]string) error {
	return setProjectFamiliesTx(ctx, db, families)
}

// SaveProjectFamily creates, replaces, or renames one family atomically.
// previousName is empty for a create; otherwise that family must exist. A
// create or rename never merges into an existing family because silently
// combining their context boundaries would be surprising. The submitted
// member set replaces the old set in full and must contain at least one slug.
func SaveProjectFamily(ctx context.Context, db *sql.DB, previousName, name string, members []string) ([]string, error) {
	previousName = strings.TrimSpace(previousName)
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("store.SaveProjectFamily: family name is empty")
	}
	members = dedupeSlugs(members)
	if len(members) == 0 {
		return nil, fmt.Errorf("store.SaveProjectFamily: %w", ErrFamilyNoMembers)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store.SaveProjectFamily: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	families, err := projectFamiliesTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if previousName == "" {
		if _, exists := families[name]; exists {
			return nil, fmt.Errorf("store.SaveProjectFamily: %q: %w", name, ErrFamilyExists)
		}
	} else {
		if _, exists := families[previousName]; !exists {
			return nil, fmt.Errorf("store.SaveProjectFamily: %q: %w", previousName, ErrFamilyNotFound)
		}
		if previousName != name {
			if _, exists := families[name]; exists {
				return nil, fmt.Errorf("store.SaveProjectFamily: %q: %w", name, ErrFamilyExists)
			}
			delete(families, previousName)
		}
	}
	families[name] = members
	if err := setProjectFamiliesTx(ctx, tx, families); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store.SaveProjectFamily: commit: %w", err)
	}
	return members, nil
}

// setProjectFamiliesTx canonicalizes and persists families via any executor, so a
// mutator can write back inside the transaction it read under.
func setProjectFamiliesTx(ctx context.Context, q settingsExecutor, families map[string][]string) error {
	clean := make(map[string][]string, len(families))
	for name, members := range families {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if deduped := dedupeSlugs(members); len(deduped) > 0 {
			clean[name] = deduped
		}
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return fmt.Errorf("store.SetProjectFamilies: %w", err)
	}
	return setSettingTx(ctx, q, SettingProjectFamilies, string(b))
}

// AddFamilyMembers adds slugs to the named family, creating the family when it is
// new, and persists the result. Existing members keep their order and duplicates
// are ignored, so callers may re-add the same slugs idempotently. Returns the
// family's resulting members.
//
// The read-decode-mutate-write runs inside one transaction (the AddRepoMapping
// recipe): the pool is capped at a single connection (see Open), so the whole
// mutation is serialized against concurrent mutators and two callers growing
// different families at once can no longer clobber each other's family. If the
// pool ever grows past one connection this needs BEGIN IMMEDIATE, since two
// deferred transactions could still interleave read-then-write.
func AddFamilyMembers(ctx context.Context, db *sql.DB, family string, slugs []string) ([]string, error) {
	family = strings.TrimSpace(family)
	if family == "" {
		return nil, fmt.Errorf("store.AddFamilyMembers: family name is empty")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store.AddFamilyMembers: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	families, err := projectFamiliesTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	members := dedupeSlugs(append(families[family], slugs...))
	families[family] = members
	if err := setProjectFamiliesTx(ctx, tx, families); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store.AddFamilyMembers: commit: %w", err)
	}
	return members, nil
}

// RemoveFamilyMembers removes slugs from the named family and persists the
// result. Passing no slugs removes the whole family; a family left with no
// members after the removal is dropped as well. Returns the family's resulting
// members, empty when the family was removed. It errors with ErrFamilyNotFound
// when the named family does not exist.
//
// Like AddFamilyMembers the read-decode-mutate-write runs inside one transaction,
// serialized by the single-connection pool (see Open), so a concurrent removal
// from another family survives instead of being clobbered by this write-back.
// If the pool ever grows past one connection this needs BEGIN IMMEDIATE.
func RemoveFamilyMembers(ctx context.Context, db *sql.DB, family string, slugs []string) ([]string, error) {
	family = strings.TrimSpace(family)
	if family == "" {
		return nil, fmt.Errorf("store.RemoveFamilyMembers: family name is empty")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store.RemoveFamilyMembers: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	families, err := projectFamiliesTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if _, ok := families[family]; !ok {
		// Rollback of the read-only transaction is harmless.
		return nil, fmt.Errorf("store.RemoveFamilyMembers: %q: %w", family, ErrFamilyNotFound)
	}
	var kept []string
	if len(slugs) == 0 {
		delete(families, family) // no slugs named: the whole family goes
	} else {
		drop := make(map[string]bool, len(slugs))
		for _, s := range slugs {
			drop[strings.TrimSpace(s)] = true
		}
		for _, m := range families[family] {
			if !drop[m] {
				kept = append(kept, m)
			}
		}
		families[family] = kept // setProjectFamiliesTx drops it when kept is empty
	}
	if err := setProjectFamiliesTx(ctx, tx, families); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store.RemoveFamilyMembers: commit: %w", err)
	}
	return kept, nil
}

// dedupeSlugs returns slugs with surrounding whitespace trimmed, blanks dropped,
// and duplicates removed, preserving first-seen order.
func dedupeSlugs(slugs []string) []string {
	seen := make(map[string]bool, len(slugs))
	out := make([]string, 0, len(slugs))
	for _, s := range slugs {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// SettingBriefingConfig is the settings key holding the console-saved briefing
// override: a JSON-encoded config.Briefing. When present it layers over the
// file/env briefing config (see BriefingConfig), so the owner can tune the
// SessionStart injection from the console without editing seamless.yaml or
// restarting the daemon.
const SettingBriefingConfig = "briefing_config"

// BriefingConfig returns the effective briefing config: base (the file/env
// values) with the console-saved override row, when present, decoded over it.
// overridden reports whether such a row exists. Absent fields in a stored
// override keep their base value, so a row written by an older console version
// stays forward-compatible.
func BriefingConfig(ctx context.Context, db *sql.DB, base config.Briefing) (cfg config.Briefing, overridden bool, err error) {
	raw, found, err := GetSetting(ctx, db, SettingBriefingConfig)
	if err != nil {
		return base, false, err
	}
	if !found || strings.TrimSpace(raw) == "" {
		return base, false, nil
	}
	cfg = base
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return base, false, fmt.Errorf("store.BriefingConfig: decode: %w", err)
	}
	return cfg, true, nil
}

// SetBriefingConfig persists b as the console briefing override. Callers
// validate first (config.Briefing.Validate); this only encodes and stores.
func SetBriefingConfig(ctx context.Context, db *sql.DB, b config.Briefing) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("store.SetBriefingConfig: %w", err)
	}
	return SetSetting(ctx, db, SettingBriefingConfig, string(raw))
}

// ClearBriefingConfig removes the console briefing override, reverting the
// effective briefing config to the file/env base.
func ClearBriefingConfig(ctx context.Context, db *sql.DB) error {
	return DeleteSetting(ctx, db, SettingBriefingConfig)
}

// GetSetting returns the value for a settings key. found is false when unset.
func GetSetting(ctx context.Context, db *sql.DB, key string) (string, bool, error) {
	return getSettingTx(ctx, db, key)
}

// getSettingTx reads a settings key via any executor. found is false when unset.
func getSettingTx(ctx context.Context, q settingsExecutor, key string) (string, bool, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
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
	return setSettingTx(ctx, db, key, value)
}

// setSettingTx upserts a settings key/value via any executor.
func setSettingTx(ctx context.Context, q settingsExecutor, key, value string) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store.SetSetting: %w", err)
	}
	return nil
}

// DeleteSetting removes a settings key. Deleting an absent key is a no-op.
func DeleteSetting(ctx context.Context, db *sql.DB, key string) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("store.DeleteSetting: %w", err)
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
// resolve without churning the setting. The read-decode-write runs inside one
// transaction: the pool is capped at a single connection (see Open), so the
// whole mutation is serialized against concurrent mutators and two agents
// registering different repos at once can no longer clobber each other's entry.
func AddRepoMapping(ctx context.Context, db *sql.DB, repoPath, slug string) error {
	repoPath = filepath.Clean(repoPath)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store.AddRepoMapping: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	m := map[string]string{}
	var raw string
	err = tx.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, SettingRepoProjectMap).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Unset: start from an empty map.
	case err != nil:
		return fmt.Errorf("store.AddRepoMapping: %w", err)
	case strings.TrimSpace(raw) != "":
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return fmt.Errorf("store.AddRepoMapping: decode: %w", err)
		}
	}
	if m[repoPath] == slug {
		return nil // rollback of the read-only transaction is harmless
	}
	m[repoPath] = slug
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("store.AddRepoMapping: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		SettingRepoProjectMap, string(b)); err != nil {
		return fmt.Errorf("store.AddRepoMapping: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store.AddRepoMapping: commit: %w", err)
	}
	return nil
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
	// A linked worktree (git worktree add, the Claude/Codex apps' managed
	// worktrees) is a checkout of the main repository, not a repository of its
	// own: key project identity on the main checkout so a session whose first
	// contact is a worktree inherits the repo's project instead of registering
	// a transient project named after the worktree directory.
	main := gitMainWorktreeRoot(root)
	if main != root {
		if slug := matchProjectPath(main, m); slug != "" {
			// Main checkout already mapped: adopt its project. An out-of-tree
			// worktree also gets its own map entry so read paths (briefing,
			// prompt recall) resolve it without re-deriving git identity.
			if _, err := EnsureProject(ctx, db, slug, slug); err != nil {
				return "", err
			}
			if !pathHasPrefix(root, main) {
				if err := AddRepoMapping(ctx, db, root, slug); err != nil {
					return "", err
				}
			}
			return slug, nil
		}
	}
	name := filepath.Base(main)
	slug := uniqueProjectSlug(core.Slugify(name), main, m)
	if _, err := EnsureProject(ctx, db, slug, name); err != nil {
		return "", err
	}
	if err := AddRepoMapping(ctx, db, main, slug); err != nil {
		return "", err
	}
	if main != root && !pathHasPrefix(root, main) {
		if err := AddRepoMapping(ctx, db, root, slug); err != nil {
			return "", err
		}
	}
	return slug, nil
}

// gitRepoRoot returns the nearest ancestor of dir (inclusive) containing a .git
// entry -- the repository root -- or "" if dir is not inside a git repo. A .git
// file (worktrees, submodules) counts as well as a .git directory.
func gitRepoRoot(dir string) string {
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

// gitMainWorktreeRoot resolves a linked-worktree root to the root of its main
// checkout. A linked worktree's .git is a file ("gitdir: <admin dir>") whose
// admin dir lives under the main repository's .git/worktrees/<name>/, and the
// commondir file inside it points back at the shared .git directory; the main
// checkout root is that directory's parent. Everything else resolves to root
// unchanged: a regular checkout (.git directory), a submodule (gitdir under
// .git/modules/, no commondir file -- a genuinely separate repository), a bare
// main repo (commondir not named .git, so there is no main checkout), or an
// unparseable/stale layout. Pure filesystem -- hooks and session_start must not
// depend on a git executable.
func gitMainWorktreeRoot(root string) string {
	gitPath := filepath.Join(root, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil || info.IsDir() {
		return root
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return root
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return root
	}
	gitdir = strings.TrimSpace(gitdir)
	if gitdir == "" {
		return root
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(root, gitdir)
	}
	common, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return root
	}
	commonDir := strings.TrimSpace(string(common))
	if commonDir == "" {
		return root
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitdir, commonDir)
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
