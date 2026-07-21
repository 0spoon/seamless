// Package config loads Seamless configuration from a single YAML file with
// SEAMLESS_* environment overrides. Env wins over file; file wins over defaults.
//
// Deliberately halved from Seam v1: no JWT/auth/multi-user config, no ChromaDB.
// Auth is a single static bearer key; vectors live in SQLite.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// LLM provider identifiers.
const (
	ProviderOpenAI    = "openai"
	ProviderOllama    = "ollama"
	ProviderAnthropic = "anthropic"
)

// Config is the fully-resolved Seamless configuration.
type Config struct {
	// Addr is the HTTP bind address (host:port). Defaults to 127.0.0.1:8081.
	Addr string `yaml:"addr"`
	// DataDir holds the SQLite database and markdown trees. A leading ~ expands.
	DataDir string `yaml:"data_dir"`

	MCP         MCP         `yaml:"mcp"`
	Budgets     Budgets     `yaml:"budgets"`
	Briefing    Briefing    `yaml:"briefing"`
	Search      Search      `yaml:"search"`
	LLM         LLM         `yaml:"llm"`
	Gardener    Gardener    `yaml:"gardener"`
	Capture     Capture     `yaml:"capture"`
	PlanCapture PlanCapture `yaml:"plan_capture"`

	// sourcePath records which config file was loaded (empty = defaults only).
	sourcePath string `yaml:"-"`
}

// MCP holds the static bearer key guarding /api/mcp and the console.
type MCP struct {
	APIKey string `yaml:"api_key"`
}

// Budgets holds token budgets for retrieval.
type Budgets struct {
	MaxBriefingTokens  int `yaml:"max_briefing_tokens"`
	RecallBudgetTokens int `yaml:"recall_budget_tokens"`
	// ToolEventMaxChars caps each captured field (tool-call args value, result,
	// hook prompt, session findings) of an Interactions transport event at this
	// many runes. 0 = unlimited (the default): content is stored in full, and the
	// tool-event retention prune -- not truncation -- is the growth control.
	ToolEventMaxChars int `yaml:"tool_event_max_chars"`
}

// Briefing tunes what the SessionStart briefing auto-injects: how many items
// each section carries, recency filters, related-project cross-over, and the
// hard-cap multiplier. Defaults reproduce the historical hardcoded behavior.
// The JSON tags back the console's runtime override row (see
// store.BriefingConfig), which layers on top of this file/env base.
//
// The never-drop invariant: constraints and active-plan rollups are exempt
// from every knob here, and so is a pinned stage while its Status header marks
// a live gate -- recency and count filters apply only to the memory index,
// findings, and sibling sections. A stage with no live gate holds its pin only
// through the StageUnknownMaxAgeDays grace window.
type Briefing struct {
	// MemoryMaxAgeDays drops memory-index lines not updated within this many
	// days. 0 = no recency filter. Constraints and stages are exempt.
	MemoryMaxAgeDays int `yaml:"memory_max_age_days" json:"memoryMaxAgeDays"`
	// MemoryMaxItems caps the memory-index line count before budget packing.
	// 0 = budget-only (no cap).
	MemoryMaxItems int `yaml:"memory_max_items" json:"memoryMaxItems"`
	// FindingsCount is how many recent findings to inject. 0 hides the section.
	FindingsCount int `yaml:"findings_count" json:"findingsCount"`
	// FindingsMaxAgeDays drops findings older than this many days. 0 = no filter.
	FindingsMaxAgeDays int `yaml:"findings_max_age_days" json:"findingsMaxAgeDays"`
	// ReadyTasksShown is how many ready-task titles the ready line names.
	// 0 hides the line entirely.
	ReadyTasksShown int `yaml:"ready_tasks_shown" json:"readyTasksShown"`
	// PendingPlanMaxDays is how far back captured-but-unapproved Claude Code
	// plans earn "awaiting approval" lines. 0 = no age cutoff.
	PendingPlanMaxDays int `yaml:"pending_plan_max_days" json:"pendingPlanMaxDays"`
	// StageUnknownMaxAgeDays is the grace window (days since last update) a
	// stage memory stays pinned when its Status header is missing or not a live
	// gate (open/in_progress/blocked). Past it the stage leaves the briefing --
	// recall still finds it. 0 = pin forever (the historical behavior).
	StageUnknownMaxAgeDays int `yaml:"stage_unknown_max_age_days" json:"stageUnknownMaxAgeDays"`
	// HardCapMultiplier times budgets.max_briefing_tokens is the absolute
	// truncation ceiling. 0 falls back to 2.
	HardCapMultiplier int `yaml:"hard_cap_multiplier" json:"hardCapMultiplier"`
	// IncludeParentMemories folds a shared parent project's active memories
	// into a child project's briefing (the historical automatic behavior).
	IncludeParentMemories bool `yaml:"include_parent_memories" json:"includeParentMemories"`
	// SiblingFindingsCount is how many recent findings from family-member
	// projects to inject. 0 hides the section.
	SiblingFindingsCount int `yaml:"sibling_findings_count" json:"siblingFindingsCount"`
	// IncludeSiblingMemories folds family-member projects' active memories
	// (constraints and stages excluded) into the briefing as a low-priority
	// "Sibling memories" section. Off by default to avoid crowding.
	IncludeSiblingMemories bool `yaml:"include_sibling_memories" json:"includeSiblingMemories"`
	// UtilityWeight is utility's share of the briefing memory-index sort key:
	// (1-w)*recency + w*utility, both half-life-decayed to [0,1). 0 = pure
	// recency (the legacy order); constraints, stages, and favorites are pinned
	// regardless. Applies only where utility ranking is active (UtilityMode).
	UtilityWeight float64 `yaml:"utility_weight" json:"utilityWeight"`
	// UtilityMode gates the briefing's utility re-ordering: "auto" (default)
	// activates per project once the gardener's readiness latch trips, "on"
	// activates everywhere immediately, "off" disables it everywhere. The
	// bounded recall/prompt-recall boosts are not gated by this.
	UtilityMode string `yaml:"utility_mode" json:"utilityMode"`
}

// UtilityModes are the accepted briefing.utility_mode values.
var UtilityModes = []string{"auto", "on", "off"}

// Validate rejects hard-invalid briefing knobs. Shared by the file/env load
// path and the console's settings form.
func (b Briefing) Validate() error {
	for _, f := range []struct {
		name string
		v    int
	}{
		{"memory_max_age_days", b.MemoryMaxAgeDays},
		{"memory_max_items", b.MemoryMaxItems},
		{"findings_count", b.FindingsCount},
		{"findings_max_age_days", b.FindingsMaxAgeDays},
		{"ready_tasks_shown", b.ReadyTasksShown},
		{"pending_plan_max_days", b.PendingPlanMaxDays},
		{"stage_unknown_max_age_days", b.StageUnknownMaxAgeDays},
		{"hard_cap_multiplier", b.HardCapMultiplier},
		{"sibling_findings_count", b.SiblingFindingsCount},
	} {
		if f.v < 0 {
			return fmt.Errorf("config: briefing.%s must be >= 0", f.name)
		}
	}
	if b.UtilityWeight < 0 || b.UtilityWeight > 1 {
		return fmt.Errorf("config: briefing.utility_weight must be in [0, 1]")
	}
	// Absent (empty) means the default; present-but-unrecognized is an error,
	// never a silent fallback.
	if b.UtilityMode != "" && !slices.Contains(UtilityModes, b.UtilityMode) {
		return fmt.Errorf("config: briefing.utility_mode invalid %q: valid values are %s",
			b.UtilityMode, strings.Join(UtilityModes, ", "))
	}
	return nil
}

// Search tunes the human-facing console search (retrieve.Search). Agent-facing
// recall is deliberately not covered: an agent can judge a weak hit for itself,
// but an observer reads "20 results" as 20 matches.
type Search struct {
	// SemanticFloor is the minimum cosine similarity a semantic-only hit needs
	// to appear in search results; hits the lexical leg also matched are exempt.
	// Without it the cosine leg is pure nearest-neighbor -- there is always a
	// "nearest" item, so any query fills the page. 0 disables the floor. Useful
	// values depend on the embedding model; the default suits OpenAI
	// text-embedding-3-*.
	SemanticFloor float64 `yaml:"semantic_floor"`
}

// LLM configures chat (digests) and embeddings. OpenAI is the default provider.
type LLM struct {
	Provider  string    `yaml:"provider"`
	OpenAI    OpenAI    `yaml:"openai"`
	Ollama    Ollama    `yaml:"ollama"`
	Anthropic Anthropic `yaml:"anthropic"`
}

// OpenAI is the first-class provider (chat + embeddings).
type OpenAI struct {
	APIKey         string `yaml:"api_key"`
	BaseURL        string `yaml:"base_url"`
	ChatModel      string `yaml:"chat_model"`
	EmbeddingModel string `yaml:"embedding_model"`
	// EmbeddingDims is the model's native dimensionality; 0 = auto-detect from
	// the first embedding response.
	EmbeddingDims int `yaml:"embedding_dims"`
}

// Ollama is the local provider (chat + embeddings).
type Ollama struct {
	BaseURL        string `yaml:"base_url"`
	ChatModel      string `yaml:"chat_model"`
	EmbeddingModel string `yaml:"embedding_model"`
	EmbeddingDims  int    `yaml:"embedding_dims"`
}

// Anthropic is a chat-only provider (no embeddings API).
type Anthropic struct {
	APIKey    string `yaml:"api_key"`
	BaseURL   string `yaml:"base_url"`
	ChatModel string `yaml:"chat_model"`
}

// Capture configures the SSRF-guarded URL fetch behind the capture_url tool.
// Unrelated to PlanCapture, which is about Claude Code plan mode.
type Capture struct {
	// AllowedPorts are the only destination ports capture_url may dial, enforced
	// on the initial URL and on every redirect hop. Empty is deliberately NOT
	// "any port": an unset key, an explicit `allowed_ports: []`, or an empty env
	// override all fall back to the 80/443 default, so the SSRF port guard cannot
	// be switched off by omission. Ports outside 1-65535 are rejected by Validate.
	AllowedPorts []int `yaml:"allowed_ports"`
}

// defaultAllowedPorts returns the built-in capture.allowed_ports value, freshly
// allocated so no caller can mutate a shared default. The capture package keeps
// the same list as its own last-resort fallback (it cannot import config).
func defaultAllowedPorts() []int { return []int{80, 443} }

// PlanCapture configures capturing Claude Code plan-mode iterations and
// planning subagents into notes via the PostToolUse/SubagentStop hooks.
type PlanCapture struct {
	// Enabled turns the plan-capture hook endpoints into no-ops when false.
	Enabled bool `yaml:"enabled"`
	// AutoTask creates a tracking task ("Implement plan: ...") when a plan is
	// approved, composing it into the plan via plan_slug.
	AutoTask bool `yaml:"auto_task"`
	// InjectRelated returns related prior plans/memories as additionalContext on
	// a session's first captured plan iteration.
	InjectRelated bool `yaml:"inject_related"`
}

// Gardener configures the propose-only maintenance passes and their ticker.
type Gardener struct {
	Enabled bool `yaml:"enabled"`
	// IntervalMinutes is the ticker period between full gardener passes.
	IntervalMinutes int `yaml:"interval_minutes"`
	// DedupThreshold is the cosine-similarity floor at/above which two active
	// memories are proposed for a merge.
	DedupThreshold float64 `yaml:"dedup_threshold"`
	// StalenessDays is the no-activity age (no update, injection, or read) beyond
	// which an active memory is proposed for archiving.
	StalenessDays int `yaml:"staleness_days"`
	// DigestDays is the trailing window of completed sessions rolled into a
	// monthly digest proposal.
	DigestDays int `yaml:"digest_days"`
	// ToolEventRetentionDays is the age beyond which transport-level Interactions
	// events (tool.call, hook.prompt) are pruned by the gardener. 0 disables the
	// prune; domain events are never pruned regardless.
	ToolEventRetentionDays int `yaml:"tool_event_retention_days"`
	// StalePlanDays is the age beyond which a captured, never-approved Claude
	// Code plan (plan-status draft/presented) is proposed for abandonment.
	// 0 disables the pass.
	StalePlanDays int `yaml:"stale_plan_days"`
	// StaleStageDays is the age (days since last update) beyond which a stage
	// memory that is not a live gate -- Status done, missing, or unrecognized --
	// is proposed for archiving. Live gates (open/in_progress/blocked) are never
	// proposed regardless of age. 0 disables the pass.
	StaleStageDays int `yaml:"stale_stage_days"`
	// SessionIdleMinutes is the no-activity age beyond which an active session
	// is considered dead: the gardener reaper expires it and the console stops
	// counting it as live. It is the single liveness threshold shared by both,
	// so the reaper cutoff and the console "live" window never drift.
	// 0 falls back to core.SessionIdleTTL (45m).
	SessionIdleMinutes int `yaml:"session_idle_minutes"`
}

// Defaults returns the built-in configuration. File and env values are layered
// on top of these, so absent keys keep their default.
func Defaults() Config {
	return Config{
		Addr:    "127.0.0.1:8081",
		DataDir: "~/.seamless",
		Budgets: Budgets{MaxBriefingTokens: 1500, RecallBudgetTokens: 1000},
		Briefing: Briefing{
			FindingsCount:          3,
			ReadyTasksShown:        3,
			PendingPlanMaxDays:     7,
			StageUnknownMaxAgeDays: 7,
			HardCapMultiplier:      2,
			IncludeParentMemories:  true,
			SiblingFindingsCount:   2,
			UtilityWeight:          0.4,
			UtilityMode:            "auto",
		},
		Search: Search{SemanticFloor: 0.3},
		LLM: LLM{
			Provider: ProviderOpenAI,
			OpenAI: OpenAI{
				BaseURL:        "https://api.openai.com/v1",
				ChatModel:      "gpt-4o",
				EmbeddingModel: "text-embedding-3-large",
				EmbeddingDims:  3072,
			},
			Ollama: Ollama{
				BaseURL:        "http://127.0.0.1:11434",
				ChatModel:      "llama3.3:latest",
				EmbeddingModel: "qwen3-embedding:8b",
				EmbeddingDims:  0,
			},
			Anthropic: Anthropic{
				BaseURL:   "https://api.anthropic.com",
				ChatModel: "claude-sonnet-5",
			},
		},
		Gardener: Gardener{
			Enabled:                true,
			IntervalMinutes:        60,
			DedupThreshold:         0.88,
			StalenessDays:          90,
			DigestDays:             30,
			ToolEventRetentionDays: 30,
			StalePlanDays:          14,
			StaleStageDays:         14,
			SessionIdleMinutes:     45,
		},
		Capture:     Capture{AllowedPorts: defaultAllowedPorts()},
		PlanCapture: PlanCapture{Enabled: true, AutoTask: true, InjectRelated: true},
	}
}

// Load resolves configuration from the first config file found in the search
// order ($SEAMLESS_CONFIG, ~/.config/seamless/seamless.yaml, ./seamless.yaml),
// then applies SEAMLESS_* environment overrides and expands paths.
func Load() (Config, error) {
	path, err := findConfigFile()
	if err != nil {
		return Config{}, err
	}
	return LoadFrom(path)
}

// LoadFrom loads defaults, overlays the YAML file at path (if non-empty), applies
// environment overrides, and expands paths. An empty path uses defaults + env.
func LoadFrom(path string) (Config, error) {
	cfg := Defaults()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("config.Load: read %s: %w", path, err)
		}
		// Unmarshal over the defaults: absent keys keep their default value.
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("config.Load: parse %s: %w", path, err)
		}
		cfg.sourcePath = path
	}

	if err := cfg.applyEnv(); err != nil {
		return Config{}, err
	}

	// An allowlist emptied by the file (`allowed_ports: []`) or by an empty env
	// override means the default, never "every port": see Capture.AllowedPorts.
	if len(cfg.Capture.AllowedPorts) == 0 {
		cfg.Capture.AllowedPorts = defaultAllowedPorts()
	}

	expanded, err := expandHome(cfg.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("config.Load: expand data_dir: %w", err)
	}
	cfg.DataDir = expanded

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects hard-invalid configuration. Soft issues (empty API keys) are
// surfaced as warnings by the doctor, not here.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Addr) == "" {
		return fmt.Errorf("config: addr is empty")
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return fmt.Errorf("config: data_dir is empty")
	}
	switch c.LLM.Provider {
	case ProviderOpenAI, ProviderOllama, ProviderAnthropic:
	default:
		return fmt.Errorf("config: unknown llm.provider %q (want openai|ollama|anthropic)", c.LLM.Provider)
	}
	if c.Budgets.MaxBriefingTokens <= 0 {
		return fmt.Errorf("config: budgets.max_briefing_tokens must be > 0")
	}
	if c.Budgets.RecallBudgetTokens <= 0 {
		return fmt.Errorf("config: budgets.recall_budget_tokens must be > 0")
	}
	if c.Budgets.ToolEventMaxChars < 0 {
		return fmt.Errorf("config: budgets.tool_event_max_chars must be >= 0")
	}
	if c.Gardener.ToolEventRetentionDays < 0 {
		return fmt.Errorf("config: gardener.tool_event_retention_days must be >= 0")
	}
	if c.Gardener.StalePlanDays < 0 {
		return fmt.Errorf("config: gardener.stale_plan_days must be >= 0")
	}
	if c.Gardener.StaleStageDays < 0 {
		return fmt.Errorf("config: gardener.stale_stage_days must be >= 0")
	}
	if c.Gardener.SessionIdleMinutes < 0 {
		return fmt.Errorf("config: gardener.session_idle_minutes must be >= 0")
	}
	for _, p := range c.Capture.AllowedPorts {
		if p < 1 || p > 65535 {
			return fmt.Errorf("config: capture.allowed_ports: %d is not a valid port (1-65535)", p)
		}
	}
	if c.Search.SemanticFloor < 0 || c.Search.SemanticFloor > 1 {
		return fmt.Errorf("config: search.semantic_floor must be in [0, 1]")
	}
	if err := c.Briefing.Validate(); err != nil {
		return err
	}
	return nil
}

// SourcePath returns the config file that was loaded, or "" if defaults+env only.
func (c Config) SourcePath() string { return c.sourcePath }

// DBPath is the SQLite database path.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "seam.db") }

// MemoryDir is the root of the per-project memory markdown tree.
func (c Config) MemoryDir() string { return filepath.Join(c.DataDir, "memory") }

// NotesDir is the root of the notes markdown tree.
func (c Config) NotesDir() string { return filepath.Join(c.DataDir, "notes") }

// applyEnv overlays SEAMLESS_* environment variables onto the config.
func (c *Config) applyEnv() error {
	envStr("SEAMLESS_ADDR", &c.Addr)
	envStr("SEAMLESS_DATA_DIR", &c.DataDir)
	envStr("SEAMLESS_MCP_API_KEY", &c.MCP.APIKey)

	if err := envInt("SEAMLESS_MAX_BRIEFING_TOKENS", &c.Budgets.MaxBriefingTokens); err != nil {
		return err
	}
	if err := envInt("SEAMLESS_RECALL_BUDGET_TOKENS", &c.Budgets.RecallBudgetTokens); err != nil {
		return err
	}
	if err := envInt("SEAMLESS_TOOL_EVENT_MAX_CHARS", &c.Budgets.ToolEventMaxChars); err != nil {
		return err
	}

	for key, dst := range map[string]*int{
		"SEAMLESS_BRIEFING_MEMORY_MAX_AGE_DAYS":        &c.Briefing.MemoryMaxAgeDays,
		"SEAMLESS_BRIEFING_MEMORY_MAX_ITEMS":           &c.Briefing.MemoryMaxItems,
		"SEAMLESS_BRIEFING_FINDINGS_COUNT":             &c.Briefing.FindingsCount,
		"SEAMLESS_BRIEFING_FINDINGS_MAX_AGE_DAYS":      &c.Briefing.FindingsMaxAgeDays,
		"SEAMLESS_BRIEFING_READY_TASKS_SHOWN":          &c.Briefing.ReadyTasksShown,
		"SEAMLESS_BRIEFING_PENDING_PLAN_MAX_DAYS":      &c.Briefing.PendingPlanMaxDays,
		"SEAMLESS_BRIEFING_STAGE_UNKNOWN_MAX_AGE_DAYS": &c.Briefing.StageUnknownMaxAgeDays,
		"SEAMLESS_BRIEFING_HARD_CAP_MULTIPLIER":        &c.Briefing.HardCapMultiplier,
		"SEAMLESS_BRIEFING_SIBLING_FINDINGS_COUNT":     &c.Briefing.SiblingFindingsCount,
	} {
		if err := envInt(key, dst); err != nil {
			return err
		}
	}
	if err := envBool("SEAMLESS_BRIEFING_INCLUDE_PARENT_MEMORIES", &c.Briefing.IncludeParentMemories); err != nil {
		return err
	}
	if err := envBool("SEAMLESS_BRIEFING_INCLUDE_SIBLING_MEMORIES", &c.Briefing.IncludeSiblingMemories); err != nil {
		return err
	}
	if err := envFloat("SEAMLESS_BRIEFING_UTILITY_WEIGHT", &c.Briefing.UtilityWeight); err != nil {
		return err
	}
	envStr("SEAMLESS_BRIEFING_UTILITY_MODE", &c.Briefing.UtilityMode)
	if err := envFloat("SEAMLESS_SEARCH_SEMANTIC_FLOOR", &c.Search.SemanticFloor); err != nil {
		return err
	}

	envStr("SEAMLESS_LLM_PROVIDER", &c.LLM.Provider)

	envStr("SEAMLESS_OPENAI_API_KEY", &c.LLM.OpenAI.APIKey)
	envStr("SEAMLESS_OPENAI_BASE_URL", &c.LLM.OpenAI.BaseURL)
	envStr("SEAMLESS_OPENAI_CHAT_MODEL", &c.LLM.OpenAI.ChatModel)
	envStr("SEAMLESS_OPENAI_EMBEDDING_MODEL", &c.LLM.OpenAI.EmbeddingModel)
	if err := envInt("SEAMLESS_OPENAI_EMBEDDING_DIMS", &c.LLM.OpenAI.EmbeddingDims); err != nil {
		return err
	}

	envStr("SEAMLESS_OLLAMA_BASE_URL", &c.LLM.Ollama.BaseURL)
	envStr("SEAMLESS_OLLAMA_CHAT_MODEL", &c.LLM.Ollama.ChatModel)
	envStr("SEAMLESS_OLLAMA_EMBEDDING_MODEL", &c.LLM.Ollama.EmbeddingModel)
	if err := envInt("SEAMLESS_OLLAMA_EMBEDDING_DIMS", &c.LLM.Ollama.EmbeddingDims); err != nil {
		return err
	}

	envStr("SEAMLESS_ANTHROPIC_API_KEY", &c.LLM.Anthropic.APIKey)
	envStr("SEAMLESS_ANTHROPIC_BASE_URL", &c.LLM.Anthropic.BaseURL)
	envStr("SEAMLESS_ANTHROPIC_CHAT_MODEL", &c.LLM.Anthropic.ChatModel)

	if err := envBool("SEAMLESS_GARDENER_ENABLED", &c.Gardener.Enabled); err != nil {
		return err
	}
	if err := envInt("SEAMLESS_GARDENER_INTERVAL_MINUTES", &c.Gardener.IntervalMinutes); err != nil {
		return err
	}
	if err := envInt("SEAMLESS_GARDENER_STALENESS_DAYS", &c.Gardener.StalenessDays); err != nil {
		return err
	}
	if err := envInt("SEAMLESS_GARDENER_DIGEST_DAYS", &c.Gardener.DigestDays); err != nil {
		return err
	}
	if err := envInt("SEAMLESS_TOOL_EVENT_RETENTION_DAYS", &c.Gardener.ToolEventRetentionDays); err != nil {
		return err
	}
	if err := envInt("SEAMLESS_GARDENER_STALE_PLAN_DAYS", &c.Gardener.StalePlanDays); err != nil {
		return err
	}
	if err := envInt("SEAMLESS_GARDENER_STALE_STAGE_DAYS", &c.Gardener.StaleStageDays); err != nil {
		return err
	}
	if err := envInt("SEAMLESS_GARDENER_SESSION_IDLE_MINUTES", &c.Gardener.SessionIdleMinutes); err != nil {
		return err
	}
	if err := envFloat("SEAMLESS_GARDENER_DEDUP_THRESHOLD", &c.Gardener.DedupThreshold); err != nil {
		return err
	}
	if err := envIntSlice("SEAMLESS_CAPTURE_ALLOWED_PORTS", &c.Capture.AllowedPorts); err != nil {
		return err
	}
	if err := envBool("SEAMLESS_PLAN_CAPTURE_ENABLED", &c.PlanCapture.Enabled); err != nil {
		return err
	}
	if err := envBool("SEAMLESS_PLAN_CAPTURE_AUTO_TASK", &c.PlanCapture.AutoTask); err != nil {
		return err
	}
	if err := envBool("SEAMLESS_PLAN_CAPTURE_INJECT_RELATED", &c.PlanCapture.InjectRelated); err != nil {
		return err
	}
	return nil
}

// findConfigFile returns the first existing config file in the search order. If
// $SEAMLESS_CONFIG is set it must exist. Returns "" when no file is found.
func findConfigFile() (string, error) {
	if p := os.Getenv("SEAMLESS_CONFIG"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("config: SEAMLESS_CONFIG=%s: %w", p, err)
		}
		return p, nil
	}
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "seamless", "seamless.yaml"))
	}
	candidates = append(candidates, "seamless.yaml")
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", nil
}

func envStr(key string, dst *string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}

func envInt(key string, dst *int) error {
	v, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("config: env %s: %w", key, err)
	}
	*dst = n
	return nil
}

// envIntSlice parses a comma-separated list of ints ("80,443,8080"), replacing
// dst wholesale rather than appending, so env stays a full override of the file
// value. Blank entries are skipped, so trailing commas and a set-but-empty value
// are tolerated; the latter yields an empty slice, which each list's own
// empty-means-default rule then resolves (it never means "unrestricted").
func envIntSlice(key string, dst *[]int) error {
	v, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}
	var out []int
	for field := range strings.SplitSeq(v, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		n, err := strconv.Atoi(field)
		if err != nil {
			return fmt.Errorf("config: env %s: %q: %w", key, field, err)
		}
		out = append(out, n)
	}
	*dst = out
	return nil
}

func envFloat(key string, dst *float64) error {
	v, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return fmt.Errorf("config: env %s: %w", key, err)
	}
	*dst = f
	return nil
}

func envBool(key string, dst *bool) error {
	v, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("config: env %s: %w", key, err)
	}
	*dst = b
	return nil
}

// expandHome expands a leading ~ to the user's home directory.
func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}
