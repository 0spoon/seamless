package main

// Claude desktop app (chat surface) MCP registration -- the third registration
// kind beside the claude CLI flow (claudeMCPAddArgs) and `codex mcp add`
// (reconcileCodexMCP). The chat surface has no management CLI: registration is
// a merge-preserving edit of claude_desktop_config.json's mcpServers, pointing
// a stdio entry at the same `seam mcp-proxy` bridge Codex uses. The file holds
// live app preferences (and possibly other servers' credentials in env), so
// every write preserves unknown keys byte-for-byte via json.RawMessage and only
// ever touches the reserved "seamless" entry. The app reads the file at
// startup, so both register and remove print a restart notice.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/files"
)

// claudeDesktopConfigPathFor is the pure per-OS resolver behind
// defaultClaudeDesktopConfigPath, split out (like serviceTeardown) so tests can
// exercise every branch on one builder. There is no Linux build of the Claude
// app, so anything but darwin/windows is an error rather than a guessed path.
func claudeDesktopConfigPathFor(goos, home, appData string) (string, error) {
	switch goos {
	case "darwin":
		if home == "" {
			return "", errors.New("claude desktop config: home directory is unknown")
		}
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"), nil
	case "windows":
		if appData == "" {
			return "", errors.New("claude desktop config: %APPDATA% is not set")
		}
		return filepath.Join(appData, "Claude", "claude_desktop_config.json"), nil
	default:
		return "", fmt.Errorf("claude desktop config: no known location on %s (the Claude app runs on macOS and Windows)", goos)
	}
}

func defaultClaudeDesktopConfigPath() (string, error) {
	return claudeDesktopConfigPathFor(runtime.GOOS, homeDir(), os.Getenv("APPDATA"))
}

// claudeDesktopMCPServer is the desired mcpServers entry: the stdio bridge with
// absolute paths only, because the app starts servers with an undefined cwd. It
// carries no env and no secret -- the bridge reads the bearer key from --config
// at connect time, the same trade every other registration kind makes.
type claudeDesktopMCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func desiredClaudeDesktopMCPServer(seamBin, configPath string) (claudeDesktopMCPServer, error) {
	if !portableAbsolutePath(seamBin) {
		return claudeDesktopMCPServer{}, fmt.Errorf("build desired Claude desktop MCP state: bridge command %q is not absolute", seamBin)
	}
	if configPath != "" && !portableAbsolutePath(configPath) {
		return claudeDesktopMCPServer{}, fmt.Errorf("build desired Claude desktop MCP state: config path %q is not absolute", configPath)
	}
	args := []string{"mcp-proxy"}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	return claudeDesktopMCPServer{Command: seamBin, Args: args}, nil
}

// claudeDesktopMCPEntry is a found mcpServers entry reduced to what the
// classifier compares. Env and unknown fields are recorded as presence only:
// a foreign entry's values may hold credentials, so no field value beyond the
// command/args we would write ever reaches a diagnostic.
type claudeDesktopMCPEntry struct {
	Command string
	Args    []string
	HasEnv  bool
	Extra   bool // fields beyond command/args/env
}

// parseClaudeDesktopMCPServer decodes one mcpServers entry. An entry that is
// not an object with a string command is unparseable -- the caller treats that
// as incompatible, since ownership cannot be established.
func parseClaudeDesktopMCPServer(raw json.RawMessage) (claudeDesktopMCPEntry, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return claudeDesktopMCPEntry{}, fmt.Errorf("parse Claude desktop MCP entry: %w", err)
	}
	if fields == nil {
		return claudeDesktopMCPEntry{}, errors.New("parse Claude desktop MCP entry: expected object")
	}
	var entry claudeDesktopMCPEntry
	commandRaw, ok := fields["command"]
	if !ok {
		return claudeDesktopMCPEntry{}, errors.New(`parse Claude desktop MCP entry: missing required field "command"`)
	}
	if err := json.Unmarshal(commandRaw, &entry.Command); err != nil {
		return claudeDesktopMCPEntry{}, fmt.Errorf(`parse Claude desktop MCP entry field "command": %w`, err)
	}
	if entry.Command == "" {
		return claudeDesktopMCPEntry{}, errors.New(`parse Claude desktop MCP entry: field "command" is empty`)
	}
	if argsRaw, has := fields["args"]; has {
		if err := json.Unmarshal(argsRaw, &entry.Args); err != nil {
			return claudeDesktopMCPEntry{}, fmt.Errorf(`parse Claude desktop MCP entry field "args": %w`, err)
		}
	}
	for key := range fields {
		switch key {
		case "command", "args":
		case "env":
			entry.HasEnv = true
		default:
			entry.Extra = true
		}
	}
	return entry, nil
}

// claudeDesktopMCPDrift mirrors codexMCPDrift for the desktop entry. The
// desired entry has no env and no extra fields, so their presence is drift; a
// repair rewrites the entry to the canonical desired form, dropping them.
func claudeDesktopMCPDrift(got claudeDesktopMCPEntry, want claudeDesktopMCPServer) []string {
	var drift []string
	if got.Command != want.Command {
		drift = append(drift, "bridge command differs")
	}
	if !slices.Equal(got.Args, want.Args) {
		drift = append(drift, "bridge arguments differ")
	}
	if got.HasEnv {
		drift = append(drift, "bridge environment present")
	}
	if got.Extra {
		drift = append(drift, "unexpected fields present")
	}
	return drift
}

// classifyClaudeDesktopMCP applies the shared exact/owned-drifted/incompatible
// vocabulary: the entry is repairable only when its command is the desired
// bridge or recognizably the seam CLI, matching codexMCPIsOwned's conservatism.
func classifyClaudeDesktopMCP(got claudeDesktopMCPEntry, want claudeDesktopMCPServer) (mcpRegClass, []string) {
	drift := claudeDesktopMCPDrift(got, want)
	if len(drift) == 0 {
		return mcpRegExact, nil
	}
	if got.Command == want.Command || isSeamCommand(got.Command) {
		return mcpRegOwnedDrifted, drift
	}
	return mcpRegIncompatible, drift
}

// errMCPServersNotObject marks a desktop config whose mcpServers key is not a
// JSON object. Registration refuses to overwrite it (it could hold servers in a
// shape we cannot see); removal treats it as holding nothing Seamless wrote.
var errMCPServersNotObject = errors.New("mcpServers is not an object")

// loadClaudeDesktopConfig decodes the desktop config into RawMessage maps so
// every value Seamless does not own round-trips byte-for-byte (an any-typed
// decode would rewrite foreign number literals). A missing file yields empty
// maps and 0600 -- other servers' entries may carry secrets in env, so a file
// this command creates starts owner-only. A present mcpServers that is not an
// object is an error: overwriting it could destroy servers we cannot see.
func loadClaudeDesktopConfig(path string) (top, servers map[string]json.RawMessage, mode os.FileMode, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, map[string]json.RawMessage{}, 0o600, nil
	}
	if err != nil {
		return nil, nil, 0, fmt.Errorf("read %s: %w", path, err)
	}
	mode = os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	top = map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &top); err != nil {
			return nil, nil, 0, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	servers = map[string]json.RawMessage{}
	if raw, ok := top["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, nil, 0, fmt.Errorf("parse %s: %w: %w", path, errMCPServersNotObject, err)
		}
		if servers == nil {
			servers = map[string]json.RawMessage{}
		}
	}
	return top, servers, mode, nil
}

// writeClaudeDesktopConfig re-serializes the config with the (possibly edited)
// servers map folded back in and writes it atomically. Key order becomes
// sorted -- the same trade hooks.writeSettings makes on settings.json; JSON
// object order carries no meaning to the app.
func writeClaudeDesktopConfig(path string, top, servers map[string]json.RawMessage, mode os.FileMode) error {
	serversJSON, err := json.Marshal(servers)
	if err != nil {
		return fmt.Errorf("marshal mcpServers: %w", err)
	}
	top["mcpServers"] = serversJSON
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	out = append(out, '\n')
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := files.AtomicWrite(path, out, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// backupClaudeDesktopConfigOnce copies the file to a timestamped sibling before
// the first Seamless change, same contract as the hooks installer's backupOnce:
// any existing Seamless backup means the true original is already preserved.
func backupClaudeDesktopConfigOnce(path string, mode os.FileMode) (string, error) {
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
		return "", fmt.Errorf("backup read: %w", err)
	}
	backup := path + ".seamless-bak-" + time.Now().UTC().Format("20060102-150405")
	if err := os.WriteFile(backup, data, mode); err != nil {
		return "", fmt.Errorf("write backup: %w", err)
	}
	return backup, nil
}

// reconcileClaudeDesktopMCP brings the desktop config's reserved entry to the
// desired stdio bridge, following reconcileCodexMCP's contract: exact is left
// untouched (no write, no backup), owned drift is repaired in place, an
// incompatible or unparseable entry is an error naming the manual fix, and a
// write is verified by re-reading the file rather than trusted.
func reconcileClaudeDesktopMCP(path, seamBin, configPath string) (mcpRegResult, error) {
	want, err := desiredClaudeDesktopMCPServer(seamBin, configPath)
	if err != nil {
		return mcpRegResult{}, err
	}
	top, servers, mode, err := loadClaudeDesktopConfig(path)
	if err != nil {
		return mcpRegResult{}, err
	}

	action := mcpRegAdded
	var priorDrift []string
	if raw, present := servers[seamlessMCPName]; present {
		got, parseErr := parseClaudeDesktopMCPServer(raw)
		if parseErr != nil {
			return mcpRegResult{}, fmt.Errorf(
				"reserved MCP name %q in %s has an unrecognized shape (%w); remove it in the Claude app (Settings > Developer > Edit Config) before rerunning install-hooks",
				seamlessMCPName, path, parseErr)
		}
		class, drift := classifyClaudeDesktopMCP(got, want)
		switch class {
		case mcpRegExact:
			return mcpRegResult{Action: mcpRegUnchanged}, nil
		case mcpRegIncompatible:
			return mcpRegResult{}, fmt.Errorf(
				"reserved MCP name %q in %s has an incompatible configuration (%s); remove it in the Claude app (Settings > Developer > Edit Config) before rerunning install-hooks",
				seamlessMCPName, path, strings.Join(drift, ", "))
		case mcpRegOwnedDrifted:
			action = mcpRegRepaired
			priorDrift = drift
		}
	}

	// A struct with fixed string fields cannot fail to marshal.
	entryJSON, _ := json.Marshal(want) //nolint:errcheck // static struct of strings; marshal cannot fail
	servers[seamlessMCPName] = entryJSON

	if _, err := backupClaudeDesktopConfigOnce(path, mode); err != nil {
		return mcpRegResult{}, err
	}
	if err := writeClaudeDesktopConfig(path, top, servers, mode); err != nil {
		return mcpRegResult{}, err
	}

	// Never report success from the write alone: re-read and compare against the
	// same desired value, symmetric with reconcileCodexMCP's post-add get.
	_, verifiedServers, _, err := loadClaudeDesktopConfig(path)
	if err != nil {
		return mcpRegResult{}, fmt.Errorf("verify Claude desktop MCP registration after write: %w", err)
	}
	raw, present := verifiedServers[seamlessMCPName]
	if !present {
		return mcpRegResult{}, errors.New("verify Claude desktop MCP registration after write: reserved name is still absent")
	}
	verified, err := parseClaudeDesktopMCPServer(raw)
	if err != nil {
		return mcpRegResult{}, fmt.Errorf("verify Claude desktop MCP registration after write: %w", err)
	}
	if drift := claudeDesktopMCPDrift(verified, want); len(drift) > 0 {
		return mcpRegResult{}, fmt.Errorf("verify Claude desktop MCP registration after write: desired state still differs (%s)", strings.Join(drift, ", "))
	}
	return mcpRegResult{Action: action, Drift: priorDrift}, nil
}

// removeClaudeDesktopMCP deletes only the reserved entry, leaving every other
// key -- including a now-empty mcpServers object -- exactly as found. A missing
// file, a missing mcpServers, a non-object mcpServers, and an absent entry are
// all "nothing to remove": uninstall never edits state it does not own, even to
// tidy it. An unparseable file is an error, because editing it blind could
// destroy live app preferences.
func removeClaudeDesktopMCP(path string) (bool, error) {
	top, servers, mode, err := loadClaudeDesktopConfig(path)
	if err != nil {
		// A non-object mcpServers holds nothing Seamless wrote; leave it alone.
		// (An absent file already loads as empty maps, not an error.)
		if errors.Is(err, errMCPServersNotObject) {
			return false, nil
		}
		return false, err
	}
	if _, present := servers[seamlessMCPName]; !present {
		return false, nil
	}
	delete(servers, seamlessMCPName)
	if _, err := backupClaudeDesktopConfigOnce(path, mode); err != nil {
		return false, err
	}
	if err := writeClaudeDesktopConfig(path, top, servers, mode); err != nil {
		return false, err
	}
	return true, nil
}

// claudeDesktopMCPSetupHint is the manual fallback when the automatic edit
// cannot run or is refused, mirroring codexAppMCPSetupHint: the exact entry to
// add by hand in the app's config editor. No secret appears -- the bridge
// carries only paths.
func claudeDesktopMCPSetupHint(seamBin, configPath string) string {
	entry := fmt.Sprintf(`"command": %q`, seamBin)
	if configPath != "" {
		entry += fmt.Sprintf(`, "args": ["mcp-proxy", "--config", %q]`, configPath)
	} else {
		entry += `, "args": ["mcp-proxy"]`
	}
	return fmt.Sprintf(
		"in the Claude app: Settings > Developer > Edit Config; add under mcpServers: %q: {%s}; save, then restart the app",
		seamlessMCPName, entry)
}

const claudeDesktopRestartNotice = "restart the Claude app to load the change (it reads the config at startup)"

// registerClaudeDesktopMCP performs the chat-surface registration and prints
// the same styled rows as the other register functions. Unlike registerClaudeMCP
// there is no CLI to be missing: a failure here is a real file problem (or an
// incompatible entry) and is returned, with the manual hint printed for repair.
func registerClaudeDesktopMCP(desktopConfig, seamBin, configPath string) error {
	path := strings.TrimSpace(desktopConfig)
	if path == "" {
		resolved, err := defaultClaudeDesktopConfigPath()
		if err != nil {
			fieldRow("mcp", yellow("incomplete (no config location)"))
			fmt.Printf("%s%s\n", fieldCont, dim(err.Error()))
			return fmt.Errorf("register Claude desktop MCP: %w", err)
		}
		path = resolved
	} else if expanded, err := expandHome(path); err == nil {
		path = expanded
	}

	result, err := reconcileClaudeDesktopMCP(path, seamBin, configPath)
	if err != nil {
		fieldRow("mcp", yellow("registration reconciliation failed"))
		fmt.Printf("%s%s\n", fieldCont, dim(err.Error()))
		fmt.Printf("%s%s\n", fieldCont, dim(claudeDesktopMCPSetupHint(seamBin, configPath)))
		return fmt.Errorf("register Claude desktop MCP: %w", err)
	}

	switch result.Action {
	case mcpRegUnchanged:
		fieldRow("mcp", dim("already registered (exact stdio bridge)")+dim("  · "+tildePath(path)))
	case mcpRegRepaired:
		fieldRow("mcp", green("repaired")+dim(" ("+strings.Join(result.Drift, ", ")+")  · "+tildePath(path)))
		fieldRow("restart", yellow(claudeDesktopRestartNotice))
	default:
		fieldRow("mcp", green("registered")+dim(" (stdio bridge: seam mcp-proxy)  · "+tildePath(path)))
		fieldRow("restart", yellow(claudeDesktopRestartNotice))
	}
	return nil
}

// deregisterClaudeDesktopMCP removes the chat-surface registration during
// uninstall, best-effort and symmetric with deregisterMCP: an unsupported OS,
// an absent file, or an absent entry is a quiet dim note, never a failure --
// uninstall with --client all must stay clean on machines without the app.
func deregisterClaudeDesktopMCP(desktopConfig string, dryRun bool) {
	path := strings.TrimSpace(desktopConfig)
	if path == "" {
		resolved, err := defaultClaudeDesktopConfigPath()
		if err != nil {
			fieldRow("mcp", dim(err.Error()))
			return
		}
		path = resolved
	} else if expanded, err := expandHome(path); err == nil {
		path = expanded
	}

	if dryRun {
		_, servers, _, err := loadClaudeDesktopConfig(path)
		if err != nil {
			fieldRow("mcp", dim("not registered  · "+tildePath(path)))
			return
		}
		if _, present := servers[seamlessMCPName]; !present {
			fieldRow("mcp", dim("not registered  · "+tildePath(path)))
			return
		}
		fieldRow("mcp", dim("would remove the seamless entry from "+tildePath(path)))
		return
	}

	removed, err := removeClaudeDesktopMCP(path)
	switch {
	case err != nil:
		fieldRow("mcp", yellow("could not deregister"))
		contDim(err.Error())
	case removed:
		fieldRow("mcp", green("deregistered")+dim("  · "+tildePath(path)))
		contDim(claudeDesktopRestartNotice)
	default:
		fieldRow("mcp", dim("not registered  · "+tildePath(path)))
	}
}
