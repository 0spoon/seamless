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

// hookSpec describes one hook Seamless installs for a client.
type hookSpec struct {
	Event    string // hooks key (SessionStart, UserPromptSubmit, Stop, ...)
	Matcher  string // "" omits the matcher key (UserPromptSubmit/Stop have none)
	Endpoint string // path appended to the base URL (the http url, and the dedup key)
	Timeout  int    // seconds (both clients' unit)
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
// Each runs `seam hook <event>` (exec form, no shell), which forwards the
// payload to Endpoint. UserPromptSubmit fires mid-turn where http is reliable,
// so it keeps an http hook (and carries the bearer key into settings.json).
//
// The plan-capture hooks (PostToolUse, SubagentStop, PermissionRequest) are
// command hooks too: the seam CLI pre-filters PostToolUse locally so the
// machine-wide Write/Edit hot path never touches the network for non-plan
// files. PostToolUse must stay a SINGLE entry (matcher-joined) -- the
// dedupe/adopt logic assumes one managed entry per event.
var seamlessHooks = []hookSpec{
	{Event: "SessionStart", Matcher: "startup|resume|clear|compact", Endpoint: "/api/hooks/session-start", Timeout: 10, CLIArg: "session-start"},
	{Event: "UserPromptSubmit", Matcher: "", Endpoint: "/api/hooks/user-prompt-submit", Timeout: 5},
	{Event: "SessionEnd", Matcher: "", Endpoint: "/api/hooks/session-end", Timeout: 10, CLIArg: "session-end"},
	{Event: "PostToolUse", Matcher: "Write|Edit|MultiEdit|ExitPlanMode", Endpoint: "/api/hooks/post-tool-use", Timeout: 10, CLIArg: "post-tool-use"},
	{Event: "SubagentStop", Matcher: "", Endpoint: "/api/hooks/subagent-stop", Timeout: 10, CLIArg: "subagent-stop"},
	{Event: "PermissionRequest", Matcher: "ExitPlanMode", Endpoint: "/api/hooks/permission-request", Timeout: 10, CLIArg: "permission-request"},
}

// codexHooks is the Codex CLI install profile (design decision D4). Codex has no
// http hook type and (as of 0.144.5) no SessionEnd event; its session end is
// reaper-driven off the per-turn Stop hook (D5). So the set is three command
// hooks -- SessionStart (briefing), UserPromptSubmit (recall), Stop (heartbeat +
// provisional harvest) -- and no plan-capture hooks: Codex has no plan-mode
// surface, so D7 keeps that CC-only. UserPromptSubmit is a command hook here (CC
// keeps it http; Codex has no http hook type). Every Codex command hook is a
// SHELL STRING, not the exec-form argv CC uses (buildEntry emits the difference).
var codexHooks = []hookSpec{
	{Event: "SessionStart", Matcher: "startup|resume|clear|compact", Endpoint: "/api/hooks/session-start", Timeout: 10, CLIArg: "session-start"},
	{Event: "UserPromptSubmit", Matcher: "", Endpoint: "/api/hooks/user-prompt-submit", Timeout: 5, CLIArg: "user-prompt-submit"},
	{Event: "Stop", Matcher: "", Endpoint: "/api/hooks/stop", Timeout: 10, CLIArg: "stop"},
}

// hookProfile returns the hook set for a client: the Codex profile for
// ClientCodex, the Claude Code profile otherwise (the default an empty client
// resolves to). The two never share a file (settings.json vs ~/.codex/hooks.json),
// so a command hook's `hook <event>` match stays unambiguous within either.
func hookProfile(client Client) []hookSpec {
	if client == ClientCodex {
		return codexHooks
	}
	return seamlessHooks
}

// InstallOptions configures an install.
type InstallOptions struct {
	Client       Client // agent client profile; "" (zero value) => Claude Code
	SettingsPath string // target file: CC settings.json or Codex hooks.json (created if absent)
	BaseURL      string // e.g. http://127.0.0.1:8081
	APIKey       string // static bearer key (written into the CC http hook header; Codex command hooks carry none)
	SeamBin      string // path to the seam CLI for command hooks; "" => "seam" (PATH)
	ConfigPath   string // abs seamless.yaml passed to command hooks as `--config` so they resolve config from any cwd; "" omits it
}

// InstallResult reports what an install did.
type InstallResult struct {
	Changed    bool
	BackupPath string   // "" when no backup was written
	Actions    []string // per-hook: "SessionStart: added|updated|unchanged"
}

// Install merges the client's Seamless hook entries into the settings/hooks file
// at opts.SettingsPath, preserving unknown keys, replacing any existing
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

	// Both clients nest event arrays under a top-level "hooks" key, so the merge
	// engine below is shared; only the hook set (hookProfile) and each entry's
	// handler shape (buildEntry) vary by client. An empty client is Claude Code.
	client := normalizeClient(string(opts.Client))
	var res InstallResult
	for _, hs := range hookProfile(client) {
		desired := buildEntry(client, hs, opts.BaseURL, opts.APIKey, opts.SeamBin, opts.ConfigPath)
		desiredURL := strings.TrimRight(opts.BaseURL, "/") + hs.Endpoint
		arr := entryArray(hooksObj, hs.Event)
		// Match every entry Seamless owns for this event: those carrying our
		// managed marker, plus any UNMARKED entry pointing at exactly this hook
		// URL or running `... hook <event>` via the seam CLI (a hand-edit, or a
		// pre-marker installer). Adopting the latter -- rather than appending
		// beside it -- is what stops re-installs from duplicating hooks. A v1
		// "seam_managed" entry at a different URL (e.g. :8080) is not matched,
		// so it is preserved untouched.
		matches := seamlessIndices(arr, desiredURL, hs.CLIArg)
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

// InstalledEvents is the set of hook events Seamless installs for a client, in
// install order. A caller (doctor) compares InstalledStatus against
// len(InstalledEvents) for the same client.
func InstalledEvents(client Client) []string {
	profile := hookProfile(normalizeClient(string(client)))
	out := make([]string, len(profile))
	for i, hs := range profile {
		out[i] = hs.Event
	}
	return out
}

// CommandHookEndpoints returns the `seam hook <arg>` events the installer wires
// as command hooks across every client profile, each mapped to the endpoint that
// hook must forward to. It is a union: the Codex profile adds user-prompt-submit
// (a command hook there, http for CC) and stop, so the CLI pin covers both
// clients. An event that both profiles wire as a command hook shares one endpoint.
//
// It exists for the seam CLI's test. The CLI keeps its own copy of this mapping
// -- it cannot import this package without dragging the store, the retriever, and
// SQLite into a binary whose job is one HTTP POST -- and a hook fails open by
// contract, so drift between the two copies is a silent no-op rather than an
// error: install-hooks would write a command line the CLI rejects, or forward to
// a route that is not there, and the only symptom would be a briefing that
// stopped arriving.
func CommandHookEndpoints() map[string]string {
	out := make(map[string]string)
	for _, profile := range [][]hookSpec{seamlessHooks, codexHooks} {
		for _, hs := range profile {
			if hs.CLIArg != "" {
				out[hs.CLIArg] = hs.Endpoint
			}
		}
	}
	return out
}

// InstalledStatus reports which of a client's Seamless-managed hook events are
// present in the settings/hooks file at path, using the same ownership test as
// Install: the managed marker, or an unmarked entry that targets the hook's URL
// under baseURL or runs `... hook <event>` via the seam CLI. The marker alone
// cannot be trusted: Claude Code re-serializes settings.json through its own
// schema when the owner edits config or permissions, dropping the
// seamless_managed key while keeping the functional entries -- those still-firing
// hooks must count as installed. A missing or empty file yields an empty slice
// and no error. The result is a subset of InstalledEvents(client), in install order.
func InstalledStatus(client Client, path, baseURL string) ([]string, error) {
	settings, _, err := loadSettings(path)
	if err != nil {
		return nil, err
	}
	hooksObj := nestedObject(settings, "hooks")
	var present []string
	for _, hs := range hookProfile(normalizeClient(string(client))) {
		desiredURL := strings.TrimRight(baseURL, "/") + hs.Endpoint
		if len(seamlessIndices(entryArray(hooksObj, hs.Event), desiredURL, hs.CLIArg)) > 0 {
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

func buildEntry(client Client, hs hookSpec, baseURL, apiKey, seamBin, configPath string) map[string]any {
	var hook map[string]any
	switch {
	case client == ClientCodex:
		// Codex command hooks are SHELL STRINGS, not exec-form argv (its config
		// schema has no `args` field -- see the codex-hook-contract-0-144-5 memory).
		// The whole invocation is one `command` string, plus a `command_windows`
		// variant carrying Windows quoting for a Windows install. Both run the same
		// `<seam> hook <event> --config <yaml> --client codex`: no bearer key is
		// written into hooks.json (the CLI loads it from --config at hook time), and
		// --client codex is what selects the Codex payload adapter server-side.
		bin := seamBin
		if bin == "" {
			bin = "seam"
		}
		hook = map[string]any{
			"type":            "command",
			"command":         codexCommand(bin, hs.CLIArg, configPath, posixQuote),
			"command_windows": codexCommand(bin, hs.CLIArg, configPath, winQuote),
			"timeout":         hs.Timeout,
		}
	case hs.CLIArg != "":
		// Command hook, EXEC form: `command` is the bare seam binary and `args`
		// carries the rest, so Claude Code spawns it directly with no shell. That
		// is the one shape that behaves identically on every OS -- on Windows CC
		// runs a shell-form command hook through PowerShell, where the old POSIX
		// string (a `VAR=value` env prefix plus single-quoting) is not valid
		// syntax. Exec form sidesteps quoting entirely: each arg is passed verbatim.
		//
		// Claude Code pipes the event JSON to stdin and injects stdout; `seam hook
		// <arg>` forwards that to Endpoint and echoes the response, so no bearer
		// key is written into settings.json. The hook fires from any cwd, so the
		// config path is passed as `--config` (exec form carries no environment, so
		// this replaces the old SEAMLESS_CONFIG env prefix) -- otherwise the CLI's
		// cwd-relative config search misses seamless.yaml and it can't authenticate.
		bin := seamBin
		if bin == "" {
			bin = "seam"
		}
		args := []any{"hook", hs.CLIArg}
		if configPath != "" {
			args = append(args, "--config", configPath)
		}
		hook = map[string]any{
			"type":    "command",
			"command": bin,
			"args":    args,
			"timeout": hs.Timeout,
		}
	default:
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

// codexCommand builds a Codex command-hook shell string:
//
//	<seam> hook <event> [--config <yaml>] --client codex
//
// quote is the shell quoter for the target OS (posixQuote for `command`,
// winQuote for `command_windows`) so a binary or config path with a space is
// not word-split by the shell Codex runs the string through. The `hook <event>`
// token is left unquoted so entryRunsHookCommand can still recognize a
// Seamless-owned entry whose marker was stripped.
func codexCommand(seamBin, cliArg, configPath string, quote func(string) string) string {
	parts := []string{quote(seamBin), "hook", cliArg}
	if configPath != "" {
		parts = append(parts, "--config", quote(configPath))
	}
	parts = append(parts, "--client", string(ClientCodex))
	return strings.Join(parts, " ")
}

// posixQuote single-quotes s for a POSIX shell, escaping any embedded single
// quote (path -> '...'\”...'). Codex runs command hooks as shell strings, so an
// unquoted path with a space would split into separate argv.
func posixQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// winQuote double-quotes s for cmd.exe / PowerShell. Windows paths cannot
// contain a double quote, so no escaping is needed.
func winQuote(s string) string {
	return `"` + s + `"`
}

// seamlessIndices returns the ascending indices of entries in arr that Seamless
// owns for a hook: entries carrying the managed marker, plus unmarked entries
// whose hook URL is desiredURL or whose command is a seam-CLI `hook <cliArg>`
// invocation (written by hand or by a pre-marker installer). A v1
// "seam_managed" entry at another URL is not matched, so it survives.
func seamlessIndices(arr []any, desiredURL, cliArg string) []int {
	var out []int
	for i, e := range arr {
		if isManaged(e) || entryTargetsURL(e, desiredURL) || entryRunsHookCommand(e, cliArg) {
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

// entryRunsHookCommand reports whether any command hook in the entry invokes
// the seam CLI for cliArg -- identifying a Seamless command hook written without
// the marker, whatever binary path it carries. Command hooks have no URL, so
// without this an unmarked command entry would be duplicated instead of adopted.
// Both hook shapes are recognized so an upgrade adopts either in place:
//
//   - exec form (CC): `args` carry "hook" then <cliArg> as adjacent elements,
//     with the binary in `command`.
//   - shell-string form (Codex, pre-exec CC installs, and hand edits): the whole
//     invocation is one `command` string containing " hook <cliArg>" as a token
//     -- at the end (old CC shell form, possibly with a SEAMLESS_CONFIG= prefix)
//     or followed by more flags (Codex: `... hook <cliArg> --config ... --client
//     codex`).
func entryRunsHookCommand(e any, cliArg string) bool {
	if cliArg == "" {
		return false
	}
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
		if hookArgsRunEvent(hm["args"], cliArg) {
			return true
		}
		if c, ok := hm["command"].(string); ok && shellCmdRunsEvent(c, cliArg) {
			return true
		}
	}
	return false
}

// shellCmdRunsEvent reports whether a shell-string command runs `seam hook
// <cliArg>` -- the " hook <cliArg>" token appears followed by end-of-string or a
// space (a trailing flag). The space/end boundary keeps `hook session-start`
// from matching a hypothetical `hook session-start-foo`.
func shellCmdRunsEvent(cmd, cliArg string) bool {
	cmd = strings.TrimSpace(cmd)
	marker := " hook " + cliArg
	i := strings.Index(cmd, marker)
	if i < 0 {
		return false
	}
	end := i + len(marker)
	return end == len(cmd) || cmd[end] == ' '
}

// hookArgsRunEvent reports whether an exec-form hook's args invoke `hook
// <cliArg>` -- "hook" immediately followed by the event name, anywhere in the
// list (the installer always emits them first, but a hand edit may not).
func hookArgsRunEvent(v any, cliArg string) bool {
	args, ok := v.([]any)
	if !ok {
		return false
	}
	for i := 0; i+1 < len(args); i++ {
		if s, ok := args[i].(string); ok && s == "hook" {
			if next, ok := args[i+1].(string); ok && next == cliArg {
				return true
			}
		}
	}
	return false
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
	// Glob only fails on a malformed pattern; an unreadable dir yields no
	// matches, which correctly reads as "no backup yet" and makes one.
	if matches, _ := filepath.Glob(path + ".seamless-bak-*"); len(matches) > 0 { //nolint:errcheck
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
	// The backup is a verbatim copy, so if the file being replaced held a
	// bearer key this copy does too -- and it is the one nothing will ever
	// rewrite. It gets the same clamp as the live file.
	if err := os.WriteFile(backup, data, secretSafeMode(mode, data)); err != nil {
		return "", fmt.Errorf("hooks: write backup: %w", err)
	}
	return backup, nil
}

// secretSafeMode narrows mode to owner-only when data carries a bearer
// credential, and otherwise leaves it exactly as found.
//
// This exists because loadSettings preserves an existing file's mode, and
// Claude Code commonly creates ~/.claude/settings.json as 0644 -- so writing
// our `Authorization: Bearer <key>` header into it would persist the daemon's
// sole credential world-readable. On a shared machine that hands any other
// local account the ability to read and write the operator's entire memory
// corpus. (Audit L3.)
//
// The test is the file's content rather than "did *we* just add a key", so it
// also covers the backup copy above and the uninstall path, neither of which
// knows the key. A masking AND is deliberate: this may only ever narrow
// permissions, never widen a deliberately stricter file.
func secretSafeMode(mode os.FileMode, data []byte) os.FileMode {
	if !bytes.Contains(data, []byte("Bearer ")) {
		return mode
	}
	return mode & 0o600
}

// writeSettings atomically writes the settings map, sorted-key indented, with a
// trailing newline, preserving the file mode -- except that a file carrying a
// bearer credential is clamped to owner-only (see secretSafeMode).
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
	if err := files.AtomicWrite(path, out, secretSafeMode(mode, out)); err != nil {
		return fmt.Errorf("hooks: write settings: %w", err)
	}
	return nil
}
