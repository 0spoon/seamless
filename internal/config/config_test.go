package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "seamless.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestDefaults(t *testing.T) {
	d := Defaults()
	require.Equal(t, "127.0.0.1:8081", d.Addr)
	require.Equal(t, "~/.seamless", d.DataDir)
	require.Equal(t, ProviderOpenAI, d.LLM.Provider)
	require.Equal(t, "text-embedding-3-large", d.LLM.OpenAI.EmbeddingModel)
	require.Equal(t, 3072, d.LLM.OpenAI.EmbeddingDims)
	require.Equal(t, 1500, d.Budgets.MaxBriefingTokens)
	require.Equal(t, 1000, d.Budgets.RecallBudgetTokens)
	require.True(t, d.Gardener.Enabled)
	require.Equal(t, 60, d.Gardener.IntervalMinutes)
	require.Equal(t, 0.88, d.Gardener.DedupThreshold)
	require.Equal(t, 90, d.Gardener.StalenessDays)
	require.Equal(t, 30, d.Gardener.DigestDays)
}

func TestLoadFrom_FileOverridesDefaults(t *testing.T) {
	path := writeConfig(t, `
addr: "127.0.0.1:9099"
data_dir: "/tmp/seamless-test-abs"
llm:
  provider: ollama
  openai:
    api_key: "sk-from-file"
gardener:
  enabled: false
`)
	cfg, err := LoadFrom(path)
	require.NoError(t, err)

	require.Equal(t, "127.0.0.1:9099", cfg.Addr)
	require.Equal(t, "/tmp/seamless-test-abs", cfg.DataDir)
	require.Equal(t, ProviderOllama, cfg.LLM.Provider)
	require.Equal(t, "sk-from-file", cfg.LLM.OpenAI.APIKey)
	require.False(t, cfg.Gardener.Enabled)
	// Absent keys keep their defaults.
	require.Equal(t, "text-embedding-3-large", cfg.LLM.OpenAI.EmbeddingModel)
	require.Equal(t, 1500, cfg.Budgets.MaxBriefingTokens)
	require.Equal(t, path, cfg.SourcePath())
}

func TestLoadFrom_EnvWinsOverFile(t *testing.T) {
	path := writeConfig(t, `
addr: "127.0.0.1:9099"
llm:
  openai:
    api_key: "sk-from-file"
`)
	t.Setenv("SEAMLESS_ADDR", "127.0.0.1:7000")
	t.Setenv("SEAMLESS_OPENAI_API_KEY", "sk-from-env")
	t.Setenv("SEAMLESS_MCP_API_KEY", "static-key-123")

	cfg, err := LoadFrom(path)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:7000", cfg.Addr)
	require.Equal(t, "sk-from-env", cfg.LLM.OpenAI.APIKey)
	require.Equal(t, "static-key-123", cfg.MCP.APIKey)
}

func TestLoadFrom_EnvOnlyNoFile(t *testing.T) {
	t.Setenv("SEAMLESS_GARDENER_ENABLED", "false")
	t.Setenv("SEAMLESS_MAX_BRIEFING_TOKENS", "800")
	t.Setenv("SEAMLESS_GARDENER_STALENESS_DAYS", "45")
	t.Setenv("SEAMLESS_GARDENER_DEDUP_THRESHOLD", "0.91")
	cfg, err := LoadFrom("")
	require.NoError(t, err)
	require.False(t, cfg.Gardener.Enabled)
	require.Equal(t, 800, cfg.Budgets.MaxBriefingTokens)
	require.Equal(t, 45, cfg.Gardener.StalenessDays)
	require.Equal(t, 0.91, cfg.Gardener.DedupThreshold)
	require.Equal(t, "", cfg.SourcePath())
}

func TestLoadFrom_ExpandsHome(t *testing.T) {
	path := writeConfig(t, `data_dir: "~/seamless-home-test"`)
	cfg, err := LoadFrom(path)
	require.NoError(t, err)

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, "seamless-home-test"), cfg.DataDir)
	require.False(t, strings.Contains(cfg.DataDir, "~"))
	require.Equal(t, filepath.Join(cfg.DataDir, "seam.db"), cfg.DBPath())
	require.Equal(t, filepath.Join(cfg.DataDir, "memory"), cfg.MemoryDir())
	require.Equal(t, filepath.Join(cfg.DataDir, "notes"), cfg.NotesDir())
}

func TestLoadFrom_BadEnvInt(t *testing.T) {
	t.Setenv("SEAMLESS_MAX_BRIEFING_TOKENS", "not-a-number")
	_, err := LoadFrom("")
	require.Error(t, err)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"defaults-ok", func(*Config) {}, false},
		{"empty-addr", func(c *Config) { c.Addr = "" }, true},
		{"empty-datadir", func(c *Config) { c.DataDir = "" }, true},
		{"unknown-provider", func(c *Config) { c.LLM.Provider = "gemini" }, true},
		{"zero-briefing-budget", func(c *Config) { c.Budgets.MaxBriefingTokens = 0 }, true},
		{"negative-recall-budget", func(c *Config) { c.Budgets.RecallBudgetTokens = -1 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Defaults()
			tt.mutate(&c)
			err := c.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
