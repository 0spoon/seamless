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
	Endpoint string // path appended to the base URL
	Timeout  int    // seconds (Claude Code's unit)
}

// seamlessHooks is the set installed/removed together.
var seamlessHooks = []hookSpec{
	{Event: "SessionStart", Matcher: "startup|resume|clear|compact", Endpoint: "/api/hooks/session-start", Timeout: 10},
	{Event: "UserPromptSubmit", Matcher: "", Endpoint: "/api/hooks/user-prompt-submit", Timeout: 5},
}

// InstallOptions configures an install.
type InstallOptions struct {
	SettingsPath string // target settings.json (created if absent)
	BaseURL      string // e.g. http://127.0.0.1:8081
	APIKey       string // static bearer key written into the Authorization header
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
		desired := buildEntry(hs, opts.BaseURL, opts.APIKey)
		arr := entryArray(hooksObj, hs.Event)
		switch idx := findManaged(arr); {
		case idx < 0:
			arr = append(arr, desired)
			res.Changed = true
			res.Actions = append(res.Actions, hs.Event+": added")
		case canonicalEqual(arr[idx], desired):
			res.Actions = append(res.Actions, hs.Event+": unchanged")
		default:
			arr[idx] = desired
			res.Changed = true
			res.Actions = append(res.Actions, hs.Event+": updated")
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

func buildEntry(hs hookSpec, baseURL, apiKey string) map[string]any {
	entry := map[string]any{
		managedMarker: true,
		"hooks": []any{
			map[string]any{
				"type":    "http",
				"url":     strings.TrimRight(baseURL, "/") + hs.Endpoint,
				"timeout": hs.Timeout,
				"headers": map[string]any{"Authorization": "Bearer " + apiKey},
			},
		},
	}
	if hs.Matcher != "" {
		entry["matcher"] = hs.Matcher
	}
	return entry
}

// findManaged returns the index of the Seamless-managed entry in arr, or -1.
func findManaged(arr []any) int {
	for i, e := range arr {
		if m, ok := e.(map[string]any); ok {
			if v, ok := m[managedMarker].(bool); ok && v {
				return i
			}
		}
	}
	return -1
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
