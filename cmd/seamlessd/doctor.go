package main

import (
	"context"
	"flag"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/hooks"
	"github.com/0spoon/seamless/internal/llm"
	"github.com/0spoon/seamless/internal/mcp"
	"github.com/0spoon/seamless/internal/store"
)

// checkStatus is the outcome of a single doctor check.
type checkStatus int

const (
	statusOK checkStatus = iota
	statusWarn
	statusFail
)

func (s checkStatus) label() string {
	switch s {
	case statusOK:
		return "ok"
	case statusWarn:
		return "warn"
	default:
		return "fail"
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

	checks = append(checks, mcpToolsCheck(), hooksCheck(cfg))
	checks = append(checks, codexChecks(cfg)...)
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
		return check{statusWarn, "hooks", fmt.Sprintf("cannot select Claude Code hook profile: %v", err)}
	}
	want := len(installed)
	baseURL := hookBaseURL(cfg.Addr)
	var candidates []string
	if home, err := expandHome("~/.claude/settings.json"); err == nil {
		candidates = append(candidates, home)
	}
	candidates = append(candidates, filepath.Join(".claude", "settings.json"))

	var best check
	found := false
	for _, path := range candidates {
		status, err := hooks.InstalledStatus(hooks.InstallOptions{
			Client: hooks.ClientClaudeCode, SettingsPath: path, BaseURL: baseURL,
			APIKey: cfg.MCP.APIKey, SeamBin: resolveSeamBin(""), ConfigPath: absConfigPath(cfg.SourcePath()),
		})
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
		if len(status.Current) == want && len(status.Stale) == 0 {
			return check{statusOK, "hooks", fmt.Sprintf("%d/%d current in %s", want, want, path)}
		}
		if !found {
			best = check{statusWarn, "hooks",
				fmt.Sprintf("%d/%d current in %s%s", len(status.Current), want, path, staleHookDetail(status.Stale))}
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

// codexChecks reports the Codex CLI integration: the hooks in
// $CODEX_HOME/hooks.json and whether the seam mcp-proxy bridge is registered
// with `codex mcp`. It never FAILs -- a machine with no Codex install must not
// break `doctor` for a Claude Code user -- so an absent Codex resolves to a
// single OK "not detected" line, and every other outcome is OK or a warning.
func codexChecks(cfg config.Config) []check {
	hooksPath, herr := expandHome(defaultCodexHooksPath())
	_, codexErr := exec.LookPath("codex")
	codexOnPath := codexErr == nil

	var status hooks.InstallStatus
	var statusErr error
	if herr == nil {
		status, statusErr = hooks.InstalledStatus(hooks.InstallOptions{
			Client: hooks.ClientCodex, SettingsPath: hooksPath, BaseURL: hookBaseURL(cfg.Addr),
			APIKey: cfg.MCP.APIKey, SeamBin: resolveSeamBin(""), ConfigPath: absConfigPath(cfg.SourcePath()),
		})
	}

	// Nothing to report when Codex is neither on PATH nor has any Seamless hooks:
	// the common Claude-Code-only machine gets one quiet line, not two warnings.
	if !codexOnPath && herr == nil && statusErr == nil && len(status.Owned) == 0 {
		return []check{{statusOK, "codex", "not detected (no codex CLI or Seamless hooks in ~/.codex)"}}
	}

	installed, installedErr := hooks.InstalledEvents(hooks.ClientCodex)
	if installedErr != nil {
		return []check{{statusWarn, "codex hooks", fmt.Sprintf("cannot select Codex hook profile: %v", installedErr)}}
	}
	want := len(installed)
	var hooksChk check
	switch {
	case herr != nil:
		hooksChk = check{statusWarn, "codex hooks", fmt.Sprintf("cannot resolve hooks path: %v", herr)}
	case statusErr != nil:
		hooksChk = check{statusWarn, "codex hooks", fmt.Sprintf("cannot inspect %s: %v", hooksPath, statusErr)}
	case len(status.Current) == want && len(status.Stale) == 0:
		hooksChk = check{statusOK, "codex hooks", fmt.Sprintf("%d/%d current in %s", want, want, hooksPath)}
	case len(status.Owned) > 0:
		hooksChk = check{statusWarn, "codex hooks",
			fmt.Sprintf("%d/%d current in %s%s", len(status.Current), want, hooksPath, staleHookDetail(status.Stale))}
	default:
		hooksChk = check{statusWarn, "codex hooks", "not installed (run: seamlessd install-hooks --client codex)"}
	}
	return []check{hooksChk, codexMCPCheck()}
}

func staleHookDetail(stale []string) string {
	if len(stale) == 0 {
		return ""
	}
	return " (stale: " + strings.Join(stale, ",") + ")"
}

// codexMCPCheck reports whether the Seamless MCP bridge is registered with the
// Codex CLI (`codex mcp get seamless` exits 0). It is bounded by a short timeout
// so a slow Codex startup cannot hang `doctor`, and it warns rather than fails
// when Codex is missing or the server is not registered.
func codexMCPCheck() check {
	codex, err := exec.LookPath("codex")
	if err != nil {
		return check{statusWarn, "codex mcp", "codex CLI not found (skipped)"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, codex, "mcp", "get", "seamless").Run() == nil {
		return check{statusOK, "codex mcp", "seamless registered (seam mcp-proxy stdio bridge)"}
	}
	return check{statusWarn, "codex mcp", "seamless not registered (run: seamlessd install-hooks --client codex)"}
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
