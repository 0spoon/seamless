package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/files"
)

// managedMarker tags a hook entry as owned by Seamless, so re-installs replace it
// in place. It is deliberately distinct from v1's "seam_managed" so v1 and v2
// entries never match (and clobber) each other if they ever share a file.
const managedMarker = "seamless_managed"

// hookSpec describes one Claude Code hook Seamless installs.
type hookSpec struct {
	Event    string // settings.json hooks key (SessionStart, UserPromptSubmit)
	Matcher  string // "" omits the matcher key (UserPromptSubmit has none)
	Endpoint string // path appended to the base URL (the http url, and the dedup key)
	Timeout  int    // seconds (Claude Code's unit)
	CLIArg   string // non-empty => install a `command` hook (`<seam> hook <CLIArg>`) not http
}

// seamlessHooks is the set installed/removed together.
//
// SessionStart and SessionEnd are command hooks. Claude Code only runs
// command/mcp_tool hooks for SessionStart, so an http one is silently skipped
// and the briefing/ambient session never fire. SessionEnd does support http,
// but at process exit the fire-and-forget request races the teardown, so the
// ambient-session harvest often never lands and sessions pile up as active;
// running it as a command hook Claude Code waits on makes the harvest reliable.
// Each shells out to `seam hook <event>`, which forwards the payload to
// Endpoint. UserPromptSubmit fires mid-turn where http is reliable, so it keeps
// an http hook (and carries the bearer key into settings.json).
var seamlessHooks = []hookSpec{
	{Event: "SessionStart", Matcher: "startup|resume|clear|compact", Endpoint: "/api/hooks/session-start", Timeout: 10, CLIArg: "session-start"},
	{Event: "UserPromptSubmit", Matcher: "", Endpoint: "/api/hooks/user-prompt-submit", Timeout: 5},
	{Event: "SessionEnd", Matcher: "", Endpoint: "/api/hooks/session-end", Timeout: 10, CLIArg: "session-end"},
}

// InstallOptions configures an install.
type InstallOptions struct {
	SettingsPath string // target settings.json (created if absent)
	BaseURL      string // e.g. http://127.0.0.1:8081
	APIKey       string // static bearer key written into the Authorization header
	SeamBin      string // path to the seam CLI for command hooks; "" => "seam" (PATH)
	ConfigPath   string // abs seamless.yaml baked into command hooks as SEAMLESS_CONFIG so they resolve config from any cwd; "" omits it
}

// InstallResult reports what an install did.
type InstallResult struct {
	Changed    bool
	BackupPath string   // "" when no backup was written
	Actions    []string // per-hook: "SessionStart: added|updated|unchanged"
}

// Install merges the Seamless hook entries into the settings.json at
// opts.SettingsPath, preserving unknown keys, replacing any existing
// Seamless-managed entries in place, and backing the file up once before the
// first change. It is idempotent: an already-current file is left untouched.
func Install(opts InstallOptions) (InstallResult, error) {
	if strings.TrimSpace(opts.APIKey) == "" {
		return InstallResult{}, fmt.Errorf("hooks.Install: api key is required")
	}
	if strings.TrimSpace(opts.SettingsPath) == "" {
		return InstallResult{}, fmt.Errorf("hooks.Install: settings path is required")
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		return InstallResult{}, fmt.Errorf("hooks.Install: base URL is required")
	}

	settings, mode, err := loadSettings(opts.SettingsPath)
	if err != nil {
		return InstallResult{}, err
	}
	hooksObj := nestedObject(settings, "hooks")

	var res InstallResult
	for _, hs := range seamlessHooks {
		desired := buildEntry(hs, opts.BaseURL, opts.APIKey, opts.SeamBin, opts.ConfigPath)
		desiredURL := strings.TrimRight(opts.BaseURL, "/") + hs.Endpoint
		arr := entryArray(hooksObj, hs.Event)
		// Match every entry Seamless owns for this event: those carrying our
		// managed marker, plus any UNMARKED entry pointing at exactly this hook
		// URL (a hand-edit, or a pre-marker installer). Adopting the latter --
		// rather than appending beside it -- is what stops re-installs from
		// duplicating hooks. A v1 "seam_managed" entry at a different URL (e.g.
		// :8080) is not matched, so it is preserved untouched.
		matches := seamlessIndices(arr, desiredURL)
		switch {
		case len(matches) == 0:
			arr = append(arr, desired)
			res.Changed = true
			res.Actions = append(res.Actions, hs.Event+": added")
		case len(matches) == 1 && canonicalEqual(arr[matches[0]], desired):
			res.Actions = append(res.Actions, hs.Event+": unchanged")
		default:
			// Keep the first owned entry (replacing it with the canonical
			// desired form) and drop any other owned duplicates.
			firstWasManaged := isManaged(arr[matches[0]])
			arr[matches[0]] = desired
			arr = removeIndices(arr, matches[1:])
			res.Changed = true
			res.Actions = append(res.Actions, hs.Event+": "+matchAction(len(matches), firstWasManaged))
		}
		hooksObj[hs.Event] = arr
	}
	if !res.Changed {
		return res, nil
	}
	settings["hooks"] = hooksObj

	backup, err := backupOnce(opts.SettingsPath, mode)
	if err != nil {
		return InstallResult{}, err
	}
	res.BackupPath = backup

	if err := writeSettings(opts.SettingsPath, settings, mode); err != nil {
		return InstallResult{}, err
	}
	return res, nil
}

// InstalledEvents is the set of hook events Seamless installs, in install order.
// A caller (doctor) compares InstalledStatus against len(InstalledEvents).
func InstalledEvents() []string {
	out := make([]string, len(seamlessHooks))
	for i, hs := range seamlessHooks {
		out[i] = hs.Event
	}
	return out
}

// InstalledStatus reports which Seamless-managed hook events are present in the
// settings.json at path. A missing or empty file yields an empty slice and no
// error. The result is a subset of InstalledEvents(), in install order.
func InstalledStatus(path string) ([]string, error) {
	settings, _, err := loadSettings(path)
	if err != nil {
		return nil, err
	}
	hooksObj := nestedObject(settings, "hooks")
	var present []string
	for _, hs := range seamlessHooks {
		if findManaged(entryArray(hooksObj, hs.Event)) >= 0 {
			present = append(present, hs.Event)
		}
	}
	return present, nil
}

// loadSettings decodes settings.json into a generic map (preserving unknown
// keys) and returns the file mode to preserve. A missing file yields an empty
// map and 0o600 (the file holds a bearer key).
func loadSettings(path string) (map[string]any, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, 0o600, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("hooks: read %s: %w", path, err)
	}
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	settings := map[string]any{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &settings); err != nil {
			return nil, 0, fmt.Errorf("hooks: parse %s: %w", path, err)
		}
	}
	return settings, mode, nil
}

// nestedObject returns settings[key] as a map, or a fresh one.
func nestedObject(settings map[string]any, key string) map[string]any {
	if v, ok := settings[key].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

// entryArray returns hooksObj[event] as a slice, or nil.
func entryArray(hooksObj map[string]any, event string) []any {
	if v, ok := hooksObj[event].([]any); ok {
		return v
	}
	return nil
}

func buildEntry(hs hookSpec, baseURL, apiKey, seamBin, configPath string) map[string]any {
	var hook map[string]any
	if hs.CLIArg != "" {
		// Command hook: Claude Code pipes the event JSON to the command's stdin
		// and injects its stdout. `seam hook <arg>` forwards that to Endpoint and
		// echoes the response, so no bearer key is written into settings.json.
		// It fires from any cwd, so bake SEAMLESS_CONFIG in -- otherwise the CLI's
		// cwd-relative config search misses seamless.yaml and it can't authenticate.
		bin := seamBin
		if bin == "" {
			bin = "seam"
		}
		command := shellArg(bin) + " hook " + hs.CLIArg
		if configPath != "" {
			command = "SEAMLESS_CONFIG=" + shellArg(configPath) + " " + command
		}
		hook = map[string]any{
			"type":    "command",
			"command": command,
			"timeout": hs.Timeout,
		}
	} else {
		hook = map[string]any{
			"type":    "http",
			"url":     strings.TrimRight(baseURL, "/") + hs.Endpoint,
			"timeout": hs.Timeout,
			"headers": map[string]any{"Authorization": "Bearer " + apiKey},
		}
	}
	entry := map[string]any{
		managedMarker: true,
		"hooks":       []any{hook},
	}
	if hs.Matcher != "" {
		entry["matcher"] = hs.Matcher
	}
	return entry
}

// shellArg returns s ready to drop into the shell command string Claude Code
// runs for a command hook. Ordinary paths pass through bare for readability;
// only a value with whitespace or shell metacharacters is single-quoted (with
// embedded single quotes escaped), so a repo under "My Projects" still works.
func shellArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`&|;<>(){}*?!#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// findManaged returns the index of the first Seamless-managed entry in arr, or -1.
func findManaged(arr []any) int {
	for i, e := range arr {
		if isManaged(e) {
			return i
		}
	}
	return -1
}

// seamlessIndices returns the ascending indices of entries in arr that Seamless
// owns for a hook: entries carrying the managed marker, plus unmarked entries
// whose hook URL is desiredURL (written by hand or by a pre-marker installer).
// A v1 "seam_managed" entry at another URL is not matched, so it survives.
func seamlessIndices(arr []any, desiredURL string) []int {
	var out []int
	for i, e := range arr {
		if isManaged(e) || entryTargetsURL(e, desiredURL) {
			out = append(out, i)
		}
	}
	return out
}

// isManaged reports whether e is a hook entry carrying the Seamless managed marker.
func isManaged(e any) bool {
	m, ok := e.(map[string]any)
	if !ok {
		return false
	}
	v, ok := m[managedMarker].(bool)
	return ok && v
}

// entryTargetsURL reports whether any http hook in the entry points at url
// (trailing slash ignored) -- identifying a Seamless hook written without the marker.
func entryTargetsURL(e any, url string) bool {
	m, ok := e.(map[string]any)
	if !ok {
		return false
	}
	hooks, ok := m["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range hooks {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if u, ok := hm["url"].(string); ok && sameURL(u, url) {
			return true
		}
	}
	return false
}

// sameURL compares two hook URLs ignoring a trailing slash.
func sameURL(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

// removeIndices returns arr without the elements at the given (ascending) indices.
func removeIndices(arr []any, idxs []int) []any {
	if len(idxs) == 0 {
		return arr
	}
	drop := make(map[int]bool, len(idxs))
	for _, i := range idxs {
		drop[i] = true
	}
	out := make([]any, 0, len(arr)-len(idxs))
	for i, e := range arr {
		if !drop[i] {
			out = append(out, e)
		}
	}
	return out
}

// matchAction labels a rewrite when at least one owned entry already existed:
// collapsing duplicates, updating our own marked entry, or adopting an unmarked one.
func matchAction(n int, firstWasManaged bool) string {
	switch {
	case n > 1:
		return "deduped"
	case firstWasManaged:
		return "updated"
	default:
		return "adopted"
	}
}

// canonicalEqual compares two JSON values ignoring key order and numeric
// formatting, so a re-install that changes nothing does not rewrite the file.
func canonicalEqual(a, b any) bool {
	ca, err1 := json.Marshal(a) // encoding/json sorts map keys
	cb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(ca, cb)
}

// backupOnce copies path to a timestamped sibling the first time Seamless
// changes it, so the true original is preserved. It skips when any Seamless
// backup already exists (never overwriting the original with a modified copy) or
// when the file does not exist yet.
func backupOnce(path string, mode os.FileMode) (string, error) {
	if matches, _ := filepath.Glob(path + ".seamless-bak-*"); len(matches) > 0 {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("hooks: backup read: %w", err)
	}
	backup := path + ".seamless-bak-" + time.Now().UTC().Format("20060102-150405")
	if err := os.WriteFile(backup, data, mode); err != nil {
		return "", fmt.Errorf("hooks: write backup: %w", err)
	}
	return backup, nil
}

// writeSettings atomically writes the settings map, sorted-key indented, with a
// trailing newline, preserving the file mode.
func writeSettings(path string, settings map[string]any, mode os.FileMode) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("hooks: mkdir %s: %w", dir, err)
		}
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("hooks: marshal settings: %w", err)
	}
	out = append(out, '\n')
	if err := files.AtomicWrite(path, out, mode); err != nil {
		return fmt.Errorf("hooks: write settings: %w", err)
	}
	return nil
}
