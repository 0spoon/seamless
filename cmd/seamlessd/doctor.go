package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/0spoon/seamless/internal/config"
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
	)

	return reportChecks(checks)
}

// apiKeyCheck warns when the static bearer key is unset.
func apiKeyCheck(cfg config.Config) check {
	if strings.TrimSpace(cfg.MCP.APIKey) == "" {
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
