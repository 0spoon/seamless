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

	MCP      MCP      `yaml:"mcp"`
	Budgets  Budgets  `yaml:"budgets"`
	LLM      LLM      `yaml:"llm"`
	Gardener Gardener `yaml:"gardener"`

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
	ChatModel string `yaml:"chat_model"`
}

// Gardener toggles the propose-only maintenance passes.
type Gardener struct {
	Enabled bool `yaml:"enabled"`
}

// Defaults returns the built-in configuration. File and env values are layered
// on top of these, so absent keys keep their default.
func Defaults() Config {
	return Config{
		Addr:    "127.0.0.1:8081",
		DataDir: "~/.seamless",
		Budgets: Budgets{MaxBriefingTokens: 1500, RecallBudgetTokens: 1000},
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
			Anthropic: Anthropic{ChatModel: "claude-sonnet-5"},
		},
		Gardener: Gardener{Enabled: true},
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
	envStr("SEAMLESS_ANTHROPIC_CHAT_MODEL", &c.LLM.Anthropic.ChatModel)

	if err := envBool("SEAMLESS_GARDENER_ENABLED", &c.Gardener.Enabled); err != nil {
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
