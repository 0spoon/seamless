package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/hooks"
	"github.com/0spoon/seamless/internal/llm"
	"github.com/0spoon/seamless/internal/mcp"
	"github.com/0spoon/seamless/internal/store"
)

// checkStatus is the outcome of a single doctor check.
type checkStatus int

const (
	statusOK checkStatus = iota
	statusInfo
	statusWarn
	statusFail
)

func (s checkStatus) label() string {
	switch s {
	case statusOK:
		return "ok"
	case statusInfo:
		return "info"
	case statusWarn:
		return "warn"
	case statusFail:
		return "fail"
	default:
		return "unknown"
	}
}

// check is one line of the doctor report.
type check struct {
	status checkStatus
	name   string
	detail string
}

// doctor runs environment self-checks and prints a report. It exits non-zero
// (via a returned error) only when a check FAILs; warnings do not fail the run.
//
// P0 grows this: config loading and database reachability are added in later
// steps so the phase-0 acceptance ("doctor reports config + DB ok") is met.
func doctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	checks := []check{
		{statusOK, "binary", fmt.Sprintf("seamlessd %s runs", version)},
	}

	cfg, err := config.Load()
	if err != nil {
		checks = append(checks, check{statusFail, "config", err.Error()})
		return reportChecks(checks)
	}
	src := cfg.SourcePath()
	if src == "" {
		src = "defaults + env (no seamless.yaml found)"
	}
	checks = append(checks,
		check{statusOK, "config", "loaded from " + src},
		check{statusOK, "data_dir", cfg.DataDir},
		apiKeyCheck(cfg),
		llmCheck(cfg),
		embedderCheck(cfg),
	)

	// Database: open (creating + migrating if needed) and report schema state.
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		checks = append(checks, check{statusFail, "database", err.Error()})
		return reportChecks(checks)
	}
	defer func() { _ = db.Close() }()
	ver, verr := store.SchemaVersion(db)
	tbls, terr := store.TableCount(db)
	if verr != nil || terr != nil {
		checks = append(checks, check{statusFail, "database", "opened but could not read schema"})
	} else {
		checks = append(checks, check{statusOK, "database",
			fmt.Sprintf("%s (schema v%d, %d tables)", cfg.DBPath(), ver, tbls)})
	}

	checks = append(checks, mcpToolsCheck())
	checks = append(checks, claudeRuntimeChecks()...)
	checks = append(checks, hooksCheck(cfg))
	checks = append(checks, claudeDesktopChecks(resolveSeamBin(""), absConfigPath(cfg.SourcePath()))...)
	checks = append(checks, codexChecks(cfg, db)...)
	checks = append(checks, gardenerCheck(cfg))

	return reportChecks(checks)
}

// hooksCheck reports whether the Claude Code Seamless hooks are installed. It
// looks in the global settings (~/.claude/settings.json) and the project-scoped
// dogfood settings (./.claude/settings.json), reporting the first location that
// has all exact current definitions, or a warning when they are partial, stale,
// or absent. Claude Code may strip the ownership marker; exact functional
// definitions remain current without it.
//
// When nothing is installed AND Claude Code is not detected on this machine, it
// resolves to a quiet OK "not detected" line rather than a warning -- symmetric
// with codexChecks, so a Codex-only user is not perpetually nagged to install a
// client they do not run.
func hooksCheck(cfg config.Config) check {
	installed, err := hooks.InstalledEvents(hooks.ClientClaudeCode)
	if err != nil {
		return check{statusFail, "hooks", "cannot build desired Claude Code definitions: " + err.Error()}
	}
	var candidates []string
	if home, err := expandHome("~/.claude/settings.json"); err == nil {
		candidates = append(candidates, home)
	}
	candidates = append(candidates, filepath.Join(".claude", "settings.json"))

	var best check
	found := false
	for _, path := range candidates {
		status, err := hooks.InstalledStatus(doctorInstallOptions(hooks.ClientClaudeCode, path, cfg))
		if err != nil {
			if !found {
				best = check{statusWarn, "hooks", fmt.Sprintf("cannot inspect %s: %v", path, err)}
				found = true
			}
			continue
		}
		if len(status.Owned) == 0 {
			continue
		}
		if hookDefinitionsCurrent(installed, status) {
			return check{statusOK, "hooks", hookDefinitionDetail(path, installed, status)}
		}
		if !found {
			best = check{statusWarn, "hooks",
				hookDefinitionDetail(path, installed, status) + "; run: seamlessd install-hooks --client claude"}
			found = true
		}
	}
	if found {
		return best
	}
	if !claudeDetected() {
		return check{statusOK, "hooks", "Claude Code not detected (no claude CLI or ~/.claude)"}
	}
	return check{statusWarn, "hooks", "not installed (run: seamlessd install-hooks)"}
}

// codexChecks reports the shared local Codex host used by the desktop app, CLI,
// and IDE extension: discoverable runtime versions, hooks in
// $CODEX_HOME/hooks.json, and whether the seam mcp-proxy bridge is registered
// with `codex mcp`. Definition validity, supported trust knowledge, observed
// activity, and MCP runnability are deliberately separate results: none is a
// proxy for another. It never FAILs -- Codex is an optional client -- so a
// machine with no Codex install/config resolves to one quiet OK line.
func codexChecks(cfg config.Config, db *sql.DB) []check {
	hooksPath, herr := expandHome(defaultCodexHooksPath())

	var status hooks.InstallStatus
	var statusErr error
	if herr == nil {
		status, statusErr = hooks.InstalledStatus(
			doctorInstallOptions(hooks.ClientCodex, hooksPath, cfg))
	}

	// A CODEX_HOME/~/.codex directory counts as detection even without a CLI on
	// PATH: it may contain opted-in hook or MCP configuration that still needs a
	// visible diagnosis. Only a genuinely absent, unconfigured client is quiet.
	if !codexDetected() && herr == nil && statusErr == nil && len(status.Owned) == 0 {
		return []check{{statusOK, "codex", "not detected (no Codex CLI, initialized home, or Seamless configuration)"}}
	}

	installed, eventsErr := hooks.InstalledEvents(hooks.ClientCodex)
	var hooksChk check
	switch {
	case herr != nil:
		hooksChk = check{statusWarn, "codex hooks", fmt.Sprintf(
			"cannot resolve hooks path: %v; set HOME/CODEX_HOME, then run: seamlessd install-hooks --client codex", herr)}
	case eventsErr != nil:
		hooksChk = check{statusWarn, "codex hooks", "cannot build desired definitions: " + eventsErr.Error() +
			"; run: seamlessd install-hooks --client codex"}
	case statusErr != nil:
		hooksChk = check{statusWarn, "codex hooks", fmt.Sprintf(
			"cannot inspect %s: %v; fix or restore the JSON, then run: seamlessd install-hooks --client codex",
			hooksPath, statusErr)}
	default:
		detail := hookDefinitionDetail(hooksPath, installed, status)
		problems := recordedHookPathProblems(hooks.ClientCodex, hooksPath)
		if len(problems) > 0 {
			detail += "; not runnable: " + strings.Join(problems, ", ")
		}
		if hookDefinitionsCurrent(installed, status) && len(problems) == 0 {
			hooksChk = check{statusOK, "codex hooks", detail}
		} else {
			hooksChk = check{statusWarn, "codex hooks",
				detail + "; run: seamlessd install-hooks --client codex"}
		}
	}

	checks := codexRuntimeChecks()
	checks = append(checks,
		hooksChk,
		check{statusWarn, "codex hook trust", "trust unverified; inspect /hooks in Codex CLI; the desktop app does not expose that command"},
	)
	if db != nil {
		checks = append(checks, codexHookActivityCheck(db))
	}
	checks = append(checks, codexMCPCheck(resolveSeamBin(""), absConfigPath(cfg.SourcePath())))
	return checks
}

// doctorInstallOptions builds the same desired definition install-hooks would
// write now. It must not recover the desired binary/config from the existing
// hook: comparing an old definition with paths read from itself makes uniform
// drift tautologically current.
func doctorInstallOptions(client hooks.Client, settingsPath string, cfg config.Config) hooks.InstallOptions {
	return hooks.InstallOptions{
		Client: client, SettingsPath: settingsPath, BaseURL: hookBaseURL(cfg.Addr),
		APIKey: cfg.MCP.APIKey, SeamBin: resolveSeamBin(""), ConfigPath: absConfigPath(cfg.SourcePath()),
	}
}

func hookDefinitionsCurrent(want []string, status hooks.InstallStatus) bool {
	if len(status.Current) != len(want) || len(status.Stale) != 0 {
		return false
	}
	current := make(map[string]struct{}, len(status.Current))
	for _, event := range status.Current {
		current[event] = struct{}{}
	}
	for _, event := range want {
		if _, ok := current[event]; !ok {
			return false
		}
	}
	return true
}

func hookDefinitionDetail(path string, want []string, status hooks.InstallStatus) string {
	current := make(map[string]struct{}, len(status.Current))
	stale := make(map[string]struct{}, len(status.Stale))
	for _, event := range status.Current {
		current[event] = struct{}{}
	}
	for _, event := range status.Stale {
		stale[event] = struct{}{}
	}

	var currentNames, staleNames, missingNames []string
	for _, event := range want {
		switch {
		case hasHookEvent(current, event):
			currentNames = append(currentNames, event)
		case hasHookEvent(stale, event):
			staleNames = append(staleNames, event)
		default:
			missingNames = append(missingNames, event)
		}
	}
	return fmt.Sprintf("definitions in %s (current: %s; stale: %s; missing: %s)",
		path, hookEventNames(currentNames), hookEventNames(staleNames), hookEventNames(missingNames))
}

func hasHookEvent(set map[string]struct{}, event string) bool {
	_, ok := set[event]
	return ok
}

func hookEventNames(events []string) string {
	if len(events) == 0 {
		return "none"
	}
	return strings.Join(events, ", ")
}

// recordedHookPathProblems checks what Codex will actually execute, separately
// from desired-definition comparison. A definition can have the right shape
// and still be non-operational because its target was deleted after install.
func recordedHookPathProblems(client hooks.Client, settingsPath string) []string {
	seamBin, configPath, ok := hooks.RecordedCommandPaths(client, settingsPath)
	if !ok {
		return nil
	}
	if expanded, err := expandHome(seamBin); err == nil {
		seamBin = expanded
	}
	var problems []string
	if !commandPathExists(seamBin) {
		problems = append(problems, fmt.Sprintf("hook executable %q is missing", seamBin))
	}
	if configPath != "" {
		if expanded, err := expandHome(configPath); err == nil {
			configPath = expanded
		}
		if info, err := os.Stat(configPath); err != nil || info.IsDir() {
			problems = append(problems, fmt.Sprintf("hook config %q is missing", configPath))
		}
	}
	return problems
}

const codexActivityTimeout = 2 * time.Second

func codexHookActivityCheck(db *sql.DB) check {
	ctx, cancel := context.WithTimeout(context.Background(), codexActivityTimeout)
	defer cancel()
	event, observedAt, ok, err := latestCodexHookObservation(ctx, db)
	if err != nil {
		return check{statusWarn, "codex hook activity",
			"cannot read recent observations: " + err.Error() + "; inspect /hooks in Codex"}
	}
	if !ok {
		return check{statusInfo, "codex hook activity",
			"no SessionStart/UserPromptSubmit observation recorded; trust remains unverified"}
	}
	return check{statusInfo, "codex hook activity", fmt.Sprintf(
		"last observed %s at %s; supporting evidence only, not proof that current definitions are trusted",
		event, observedAt.UTC().Format(time.RFC3339))}
}

func latestCodexHookObservation(ctx context.Context, db *sql.DB) (string, time.Time, bool, error) {
	row := db.QueryRowContext(ctx, `
		SELECT ts, json_extract(payload, '$.hook')
		FROM events
		WHERE kind IN (?, ?)
		  AND CASE WHEN json_valid(payload) THEN
			json_extract(payload, '$.external_client') = ?
			AND json_extract(payload, '$.hook') IN (?, ?)
		  ELSE 0 END
		ORDER BY ts DESC, id DESC
		LIMIT 1`,
		string(core.EventInjected), string(core.EventHookPrompt), string(hooks.ClientCodex),
		"session-start", "user-prompt-submit")
	var tsText, hook string
	if err := row.Scan(&tsText, &hook); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", time.Time{}, false, nil
		}
		return "", time.Time{}, false, fmt.Errorf("query Codex hook activity: %w", err)
	}
	observedAt, err := core.ParseTime(tsText)
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("parse Codex hook activity timestamp: %w", err)
	}
	switch hook {
	case "session-start":
		hook = "SessionStart"
	case "user-prompt-submit":
		hook = "UserPromptSubmit"
	}
	return hook, observedAt, true, nil
}

// codexMCPCheck compares Codex's machine-readable registration with the same
// desired state used by install-hooks. It is bounded by a short timeout and
// warns rather than fails because Codex is an optional client.
func codexMCPCheck(seamBin, configPath string) check {
	codex, err := exec.LookPath("codex")
	if err != nil {
		return check{statusWarn, "codex mcp",
			"management CLI not found; MCP state is unverified and automated setup is incomplete; " +
				codexAppMCPSetupHint(seamBin, configPath)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), mcpCommandTimeout)
	defer cancel()
	return codexMCPCheckWithRunner(ctx, execMCPCommandRunner{
		client: "codex", path: codex, timeout: mcpCommandTimeout,
	}, seamBin, configPath)
}

func codexMCPCheckWithRunner(ctx context.Context, runner mcpCommandRunner, seamBin, configPath string) check {
	want, err := desiredCodexMCPState(seamBin, configPath)
	if err != nil {
		return check{statusWarn, "codex mcp", "cannot build desired registration: " + err.Error() +
			"; install the seam CLI, then run: seamlessd install-hooks --client codex"}
	}
	got, present, err := inspectCodexMCP(ctx, runner)
	if err != nil {
		return check{statusWarn, "codex mcp", "cannot inspect registration: " + err.Error() +
			"; run: codex mcp get seamless --json"}
	}
	if !present {
		return check{statusWarn, "codex mcp", "seamless not registered (run: seamlessd install-hooks --client codex)"}
	}
	class, drift := classifyCodexMCPState(got, want)
	switch class {
	case mcpRegIncompatible:
		return check{statusWarn, "codex mcp", fmt.Sprintf(
			"reserved name has an incompatible registration (%s); run: codex mcp remove seamless; then seamlessd install-hooks --client codex",
			strings.Join(drift, ", "))}
	case mcpRegOwnedDrifted:
		return check{statusWarn, "codex mcp", fmt.Sprintf(
			"owned registration is stale (%s; run: seamlessd install-hooks --client codex)",
			strings.Join(drift, ", "))}
	case mcpRegExact:
		// Continue to the local target checks below.
	default:
		return check{statusWarn, "codex mcp",
			"registration has an unknown classification; run: codex mcp get seamless --json"}
	}
	if problems := mcpBridgePathProblems(want.Transport.Command, want.Transport.Args); len(problems) > 0 {
		return check{statusWarn, "codex mcp", fmt.Sprintf(
			"exact registration is not runnable (%s; repair the targets, then run: seamlessd install-hooks --client codex)",
			strings.Join(problems, ", "))}
	}
	return check{statusOK, "codex mcp", "exact enabled stdio bridge (seam mcp-proxy); target paths exist"}
}

// claudeDesktopChecks reports the Claude app chat surface: whether the desktop
// config's reserved mcpServers entry matches the same desired stdio bridge
// install-hooks --client claude-desktop would write now -- never paths
// recovered from the entry itself, which would make uniform drift
// tautologically current. It emits no lines at all (symmetric with
// claudeRuntimeChecks) when neither the app nor a desktop config file is
// present, and it never FAILs: the chat surface is an optional, explicit
// opt-in target.
func claudeDesktopChecks(seamBin, configPath string) []check {
	path, err := defaultClaudeDesktopConfigPath()
	if err != nil {
		// No known config location (no macOS/Windows app layout, or no home):
		// the app cannot be installed here, so there is nothing to diagnose.
		return nil
	}
	return claudeDesktopChecksFor(path, claudeDesktopAppDetected(), seamBin, configPath)
}

func claudeDesktopChecksFor(path string, appDetected bool, seamBin, configPath string) []check {
	info, statErr := os.Lstat(path)
	configExists := statErr == nil && !info.IsDir()
	if !appDetected && !configExists {
		return nil
	}
	return []check{claudeDesktopMCPCheck(path, seamBin, configPath)}
}

// claudeDesktopMCPCheck compares the desktop config's reserved entry with the
// desired stdio bridge. Everything here is file evidence: the app reads the
// config only at startup and exposes no way to ask what it actually loaded, so
// even an exact entry reports the running app's state as unverifiable rather
// than assumed. An absent entry is informational, not drift -- the chat
// surface is never auto-selected by install-hooks.
func claudeDesktopMCPCheck(path, seamBin, configPath string) check {
	want, err := desiredClaudeDesktopMCPServer(seamBin, configPath)
	if err != nil {
		return check{statusWarn, "claude desktop mcp", "cannot build desired registration: " + err.Error() +
			"; install the seam CLI, then run: seamlessd install-hooks --client claude-desktop"}
	}
	_, servers, _, err := loadClaudeDesktopConfig(path)
	if err != nil {
		return check{statusWarn, "claude desktop mcp", fmt.Sprintf(
			"cannot inspect %s: %v; fix or restore the JSON, then run: seamlessd install-hooks --client claude-desktop", path, err)}
	}
	raw, present := servers[seamlessMCPName]
	if !present {
		return check{statusInfo, "claude desktop mcp",
			"chat surface not registered; opt in with: seamlessd install-hooks --client claude-desktop; or " +
				claudeDesktopMCPSetupHint(seamBin, configPath)}
	}
	got, parseErr := parseClaudeDesktopMCPServer(raw)
	if parseErr != nil {
		return check{statusWarn, "claude desktop mcp", fmt.Sprintf(
			"reserved name has an unrecognized shape in %s (%v); remove it in the Claude app (Settings > Developer > Edit Config), then run: seamlessd install-hooks --client claude-desktop",
			path, parseErr)}
	}
	class, drift := classifyClaudeDesktopMCP(got, want)
	switch class {
	case mcpRegIncompatible:
		return check{statusWarn, "claude desktop mcp", fmt.Sprintf(
			"reserved name has an incompatible registration (%s); remove it in the Claude app (Settings > Developer > Edit Config), then run: seamlessd install-hooks --client claude-desktop",
			strings.Join(drift, ", "))}
	case mcpRegOwnedDrifted:
		return check{statusWarn, "claude desktop mcp", fmt.Sprintf(
			"owned registration is stale (%s; run: seamlessd install-hooks --client claude-desktop)",
			strings.Join(drift, ", "))}
	case mcpRegExact:
		// Continue to the local target checks below.
	default:
		return check{statusWarn, "claude desktop mcp",
			"registration has an unknown classification; inspect " + path}
	}
	if problems := mcpBridgePathProblems(want.Command, want.Args); len(problems) > 0 {
		return check{statusWarn, "claude desktop mcp", fmt.Sprintf(
			"exact registration is not runnable (%s; repair the targets, then run: seamlessd install-hooks --client claude-desktop)",
			strings.Join(problems, ", "))}
	}
	return check{statusOK, "claude desktop mcp", fmt.Sprintf(
		"exact stdio bridge (seam mcp-proxy) in %s; target paths exist; whether the running app has loaded it is unverifiable (the app reads the config at startup)",
		path)}
}

// gardenerCheck reports the gardener ticker configuration.
func gardenerCheck(cfg config.Config) check {
	g := cfg.Gardener
	if !g.Enabled {
		return check{statusWarn, "gardener", "disabled (set gardener.enabled: true to run maintenance passes)"}
	}
	return check{statusOK, "gardener", fmt.Sprintf(
		"enabled (every %dm; dedup>=%.2f, staleness %dd, digest %dd)",
		g.IntervalMinutes, g.DedupThreshold, g.StalenessDays, g.DigestDays)}
}

// embedderCheck probes the configured embedder so a misconfiguration (bad key,
// unreachable Ollama) is caught before it silently degrades recall to FTS. A
// missing credential or a failed probe is a warning, not a failure -- recall
// still works lexically.
func embedderCheck(cfg config.Config) check {
	if missing, why := missingEmbedCredential(cfg); missing {
		return check{statusWarn, "embedder", why + " (recall degrades to FTS)"}
	}
	e, err := llm.NewEmbedder(cfg.LLM)
	if err != nil {
		return check{statusWarn, "embedder", "disabled: " + err.Error() + " (recall degrades to FTS)"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if _, err := e.Embed(ctx, "seamless doctor reachability probe"); err != nil {
		return check{statusWarn, "embedder", fmt.Sprintf(
			"provider=%s model=%s unreachable: %v (recall degrades to FTS)", cfg.LLM.Provider, e.Model(), err)}
	}
	return check{statusOK, "embedder", fmt.Sprintf("provider=%s model=%s reachable", cfg.LLM.Provider, e.Model())}
}

// missingEmbedCredential reports whether the selected provider lacks the
// credential it needs to embed, so doctor can skip a doomed network probe.
func missingEmbedCredential(cfg config.Config) (bool, string) {
	switch cfg.LLM.Provider {
	case config.ProviderOpenAI:
		if strings.TrimSpace(cfg.LLM.OpenAI.APIKey) == "" {
			return true, "provider=openai but openai.api_key is empty"
		}
	case config.ProviderAnthropic:
		return true, "provider=anthropic has no embeddings API"
	}
	return false, ""
}

// mcpToolsCheck asserts the MCP server registers exactly the expected number of
// tools (the P4 target is 26). It builds a throwaway server -- tool registration
// touches no external dependency -- and compares the registered count to
// mcp.ToolCount, catching a tool that was written but never wired in (or vice
// versa).
func mcpToolsCheck() check {
	srv := mcp.New(mcp.Config{})
	n := srv.NumTools()
	if n != mcp.ToolCount {
		return check{statusFail, "mcp_tools",
			fmt.Sprintf("registered %d tools but ToolCount is %d", n, mcp.ToolCount)}
	}
	return check{statusOK, "mcp_tools", fmt.Sprintf("%d tools registered", n)}
}

// apiKeyCheck warns when the static bearer key is unset. On a true first run
// (no config file at all) the message points at serve, which generates one.
func apiKeyCheck(cfg config.Config) check {
	if strings.TrimSpace(cfg.MCP.APIKey) == "" {
		if cfg.SourcePath() == "" {
			return check{statusWarn, "mcp.api_key", "empty -- `seamlessd serve` generates one on first run (or set SEAMLESS_MCP_API_KEY)"}
		}
		return check{statusWarn, "mcp.api_key", "empty -- set SEAMLESS_MCP_API_KEY (or mcp.api_key) before exposing /api/mcp"}
	}
	return check{statusOK, "mcp.api_key", "set"}
}

// llmCheck warns when the selected provider is missing the credential it needs.
func llmCheck(cfg config.Config) check {
	p := cfg.LLM.Provider
	switch p {
	case config.ProviderOpenAI:
		if strings.TrimSpace(cfg.LLM.OpenAI.APIKey) == "" {
			return check{statusWarn, "llm", "provider=openai but openai.api_key empty (chat + embeddings will fail)"}
		}
	case config.ProviderAnthropic:
		if strings.TrimSpace(cfg.LLM.Anthropic.APIKey) == "" {
			return check{statusWarn, "llm", "provider=anthropic but anthropic.api_key empty"}
		}
	case config.ProviderOllama:
		// Local; no credential required.
	}
	return check{statusOK, "llm", "provider=" + p}
}

// reportChecks prints each check and returns an error if any FAILed.
func reportChecks(checks []check) error {
	var failed int
	for _, c := range checks {
		fmt.Printf("  [%-4s] %s: %s\n", c.status.label(), c.name, c.detail)
		if c.status == statusFail {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("doctor: %d check(s) failed", failed)
	}
	return nil
}
