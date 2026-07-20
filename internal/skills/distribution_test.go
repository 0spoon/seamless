package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/0spoon/seamless/internal/agentguide"
)

func TestSkillFrontmatterAndCodexMetadata(t *testing.T) {
	for _, name := range []string{OnboardName, ResearchName} {
		skill := readAsset(t, name, "SKILL.md")
		front := frontmatter(t, skill)
		require.Equal(t, name, front["name"])
		require.NotEmpty(t, front["description"])
		require.ElementsMatch(t, []string{"name", "description"}, mapKeys(front))

		var metadata struct {
			Interface struct {
				DisplayName      string `yaml:"display_name"`
				ShortDescription string `yaml:"short_description"`
				DefaultPrompt    string `yaml:"default_prompt"`
			} `yaml:"interface"`
			Policy struct {
				AllowImplicit bool `yaml:"allow_implicit_invocation"`
			} `yaml:"policy"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(readAsset(t, name, "agents", "openai.yaml")), &metadata))
		require.NotEmpty(t, metadata.Interface.DisplayName)
		require.GreaterOrEqual(t, len(metadata.Interface.ShortDescription), 25)
		require.LessOrEqual(t, len(metadata.Interface.ShortDescription), 64)
		require.Contains(t, metadata.Interface.DefaultPrompt, "$"+name)
		if name == OnboardName {
			require.False(t, metadata.Policy.AllowImplicit)
		} else {
			require.True(t, metadata.Policy.AllowImplicit)
		}
	}
}

func TestGuidanceConceptsStayPresentAcrossServerAndOnboarding(t *testing.T) {
	onboard := readAsset(t, OnboardName, "SKILL.md")
	for _, term := range agentguide.RequiredWorkflowTerms() {
		require.Contains(t, onboard, term)
		require.Contains(t, agentguide.MCPInstructions, term)
	}
}

func TestDistributionSurfacesCarryBothClientPackages(t *testing.T) {
	repo := filepath.Join("..", "..")
	goreleaser := readRepoFile(t, repo, ".goreleaser.yaml")
	for _, asset := range []string{
		"internal/skills/assets/seam-onboard/SKILL.md",
		"internal/skills/assets/seam-onboard/agents/openai.yaml",
		"internal/skills/assets/seam-research/SKILL.md",
		"internal/skills/assets/seam-research/agents/openai.yaml",
	} {
		require.Contains(t, goreleaser, asset)
	}

	installer := readRepoFile(t, repo, "docs", "install")
	require.Contains(t, installer, "SEAMLESS_CLIENT")
	require.Contains(t, installer, "${CODEX_HOME:-$HOME/.codex}")
	// The selected client reaches install-hooks, which is what delivers the
	// skills. It rides in a variable rather than inline because --client only
	// exists from v0.3.3 and the installer probes the pinned binary for it.
	require.Contains(t, installer, `CLIENT_ARGS="--client $AGENT_CLIENT"`)
	require.Contains(t, installer, "install-hooks $CLIENT_ARGS")
	require.Contains(t, installer, "supports_client_flag")
	require.NotContains(t, installer, "install_onboard_skill")

	powershell := readRepoFile(t, repo, "docs", "install.ps1")
	require.Contains(t, powershell, "$env:SEAMLESS_CLIENT")
	require.Contains(t, powershell, "$env:CODEX_HOME")
	require.Contains(t, powershell, "$clientArgs = @('--client', $AgentClient)")
	require.Contains(t, powershell, "install-hooks @clientArgs")
	require.NotContains(t, powershell, "Install-OnboardSkill")

	makefile := readRepoFile(t, repo, "Makefile")
	require.Contains(t, makefile, "scripts/install-skill.sh seam-onboard $(CLIENT)")
	require.Contains(t, makefile, "scripts/install-skill.sh seam-research $(CLIENT)")
}

func readAsset(t *testing.T, parts ...string) string {
	t.Helper()
	name := filepath.ToSlash(filepath.Join(append([]string{"assets"}, parts...)...))
	data, err := assets.ReadFile(name)
	require.NoError(t, err)
	return string(data)
}

func readRepoFile(t *testing.T, repo string, parts ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{repo}, parts...)...))
	require.NoError(t, err)
	return string(data)
}

func frontmatter(t *testing.T, skill string) map[string]any {
	t.Helper()
	trimmed := strings.TrimPrefix(skill, "---\n")
	raw, _, ok := strings.Cut(trimmed, "\n---\n")
	require.True(t, ok, "missing YAML frontmatter terminator")
	var out map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(raw), &out))
	return out
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}
