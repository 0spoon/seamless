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
	require.Equal(t, "https://api.anthropic.com", d.LLM.Anthropic.BaseURL)
	require.Equal(t, 1500, d.Budgets.MaxBriefingTokens)
	require.Equal(t, 1000, d.Budgets.RecallBudgetTokens)
	require.Equal(t, 0, d.Budgets.ToolEventMaxChars) // 0 = unlimited
	require.True(t, d.Gardener.Enabled)
	require.Equal(t, 60, d.Gardener.IntervalMinutes)
	require.Equal(t, 0.88, d.Gardener.DedupThreshold)
	require.Equal(t, 90, d.Gardener.StalenessDays)
	require.Equal(t, 30, d.Gardener.DigestDays)
	require.Equal(t, 30, d.Gardener.ToolEventRetentionDays)
	require.Equal(t, 14, d.Gardener.StalePlanDays)
	require.Equal(t, 45, d.Gardener.SessionIdleMinutes)
	// Briefing defaults reproduce the historical hardcoded auto-inject behavior.
	require.Equal(t, 0, d.Briefing.MemoryMaxAgeDays)
	require.Equal(t, 0, d.Briefing.MemoryMaxItems)
	require.Equal(t, 3, d.Briefing.FindingsCount)
	require.Equal(t, 0, d.Briefing.FindingsMaxAgeDays)
	require.Equal(t, 3, d.Briefing.ReadyTasksShown)
	require.Equal(t, 7, d.Briefing.PendingPlanMaxDays)
	require.Equal(t, 2, d.Briefing.HardCapMultiplier)
	require.True(t, d.Briefing.IncludeParentMemories)
	require.Equal(t, 2, d.Briefing.SiblingFindingsCount)
	require.False(t, d.Briefing.IncludeSiblingMemories)
	require.True(t, d.PlanCapture.Enabled)
	require.True(t, d.PlanCapture.AutoTask)
	require.True(t, d.PlanCapture.InjectRelated)
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
	t.Setenv("SEAMLESS_TOOL_EVENT_MAX_CHARS", "4096")
	t.Setenv("SEAMLESS_TOOL_EVENT_RETENTION_DAYS", "7")
	t.Setenv("SEAMLESS_PLAN_CAPTURE_ENABLED", "false")
	t.Setenv("SEAMLESS_PLAN_CAPTURE_AUTO_TASK", "false")
	t.Setenv("SEAMLESS_PLAN_CAPTURE_INJECT_RELATED", "false")
	t.Setenv("SEAMLESS_GARDENER_STALE_PLAN_DAYS", "21")
	cfg, err := LoadFrom("")
	require.NoError(t, err)
	require.False(t, cfg.Gardener.Enabled)
	require.Equal(t, 800, cfg.Budgets.MaxBriefingTokens)
	require.Equal(t, 45, cfg.Gardener.StalenessDays)
	require.Equal(t, 0.91, cfg.Gardener.DedupThreshold)
	require.Equal(t, 4096, cfg.Budgets.ToolEventMaxChars)
	require.Equal(t, 7, cfg.Gardener.ToolEventRetentionDays)
	require.Equal(t, 21, cfg.Gardener.StalePlanDays)
	require.False(t, cfg.PlanCapture.Enabled)
	require.False(t, cfg.PlanCapture.AutoTask)
	require.False(t, cfg.PlanCapture.InjectRelated)
	require.Equal(t, "", cfg.SourcePath())
}

func TestLoadFrom_AnthropicBaseURL(t *testing.T) {
	path := writeConfig(t, `
llm:
  anthropic:
    base_url: "http://127.0.0.1:9911"
`)
	cfg, err := LoadFrom(path)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:9911", cfg.LLM.Anthropic.BaseURL)
	// Absent sibling keys keep their defaults.
	require.Equal(t, "claude-sonnet-5", cfg.LLM.Anthropic.ChatModel)

	t.Setenv("SEAMLESS_ANTHROPIC_BASE_URL", "http://127.0.0.1:9922")
	cfg, err = LoadFrom(path)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:9922", cfg.LLM.Anthropic.BaseURL, "env wins over file")
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

func TestLoadFrom_BriefingFileAndEnv(t *testing.T) {
	path := writeConfig(t, `
briefing:
  memory_max_age_days: 60
  findings_count: 5
  include_sibling_memories: true
gardener:
  session_idle_minutes: 30
`)
	cfg, err := LoadFrom(path)
	require.NoError(t, err)
	require.Equal(t, 60, cfg.Briefing.MemoryMaxAgeDays)
	require.Equal(t, 5, cfg.Briefing.FindingsCount)
	require.True(t, cfg.Briefing.IncludeSiblingMemories)
	require.Equal(t, 30, cfg.Gardener.SessionIdleMinutes)
	// Absent briefing keys keep their defaults.
	require.Equal(t, 3, cfg.Briefing.ReadyTasksShown)
	require.Equal(t, 7, cfg.Briefing.PendingPlanMaxDays)
	require.True(t, cfg.Briefing.IncludeParentMemories)

	t.Setenv("SEAMLESS_BRIEFING_MEMORY_MAX_AGE_DAYS", "90")
	t.Setenv("SEAMLESS_BRIEFING_MEMORY_MAX_ITEMS", "25")
	t.Setenv("SEAMLESS_BRIEFING_FINDINGS_MAX_AGE_DAYS", "14")
	t.Setenv("SEAMLESS_BRIEFING_READY_TASKS_SHOWN", "1")
	t.Setenv("SEAMLESS_BRIEFING_PENDING_PLAN_MAX_DAYS", "3")
	t.Setenv("SEAMLESS_BRIEFING_HARD_CAP_MULTIPLIER", "3")
	t.Setenv("SEAMLESS_BRIEFING_SIBLING_FINDINGS_COUNT", "0")
	t.Setenv("SEAMLESS_BRIEFING_INCLUDE_PARENT_MEMORIES", "false")
	t.Setenv("SEAMLESS_BRIEFING_INCLUDE_SIBLING_MEMORIES", "false")
	t.Setenv("SEAMLESS_GARDENER_SESSION_IDLE_MINUTES", "20")
	cfg, err = LoadFrom(path)
	require.NoError(t, err)
	require.Equal(t, 90, cfg.Briefing.MemoryMaxAgeDays, "env wins over file")
	require.Equal(t, 25, cfg.Briefing.MemoryMaxItems)
	require.Equal(t, 14, cfg.Briefing.FindingsMaxAgeDays)
	require.Equal(t, 1, cfg.Briefing.ReadyTasksShown)
	require.Equal(t, 3, cfg.Briefing.PendingPlanMaxDays)
	require.Equal(t, 3, cfg.Briefing.HardCapMultiplier)
	require.Equal(t, 0, cfg.Briefing.SiblingFindingsCount)
	require.False(t, cfg.Briefing.IncludeParentMemories)
	require.False(t, cfg.Briefing.IncludeSiblingMemories)
	require.Equal(t, 20, cfg.Gardener.SessionIdleMinutes)
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
		{"zero-tool-event-cap-ok", func(c *Config) { c.Budgets.ToolEventMaxChars = 0 }, false},
		{"negative-tool-event-cap", func(c *Config) { c.Budgets.ToolEventMaxChars = -1 }, true},
		{"zero-tool-event-retention-ok", func(c *Config) { c.Gardener.ToolEventRetentionDays = 0 }, false},
		{"negative-tool-event-retention", func(c *Config) { c.Gardener.ToolEventRetentionDays = -1 }, true},
		{"zero-stale-plan-days-ok", func(c *Config) { c.Gardener.StalePlanDays = 0 }, false},
		{"negative-stale-plan-days", func(c *Config) { c.Gardener.StalePlanDays = -1 }, true},
		{"zero-session-idle-ok", func(c *Config) { c.Gardener.SessionIdleMinutes = 0 }, false},
		{"negative-session-idle", func(c *Config) { c.Gardener.SessionIdleMinutes = -1 }, true},
		{"zero-briefing-knobs-ok", func(c *Config) { c.Briefing = Briefing{} }, false},
		{"negative-briefing-findings", func(c *Config) { c.Briefing.FindingsCount = -1 }, true},
		{"negative-briefing-memory-age", func(c *Config) { c.Briefing.MemoryMaxAgeDays = -1 }, true},
		{"negative-briefing-hard-cap", func(c *Config) { c.Briefing.HardCapMultiplier = -1 }, true},
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
