package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/files"
)

func TestRoot_ClientHomes(t *testing.T) {
	home := filepath.Join(t.TempDir(), "user")
	codexHome := filepath.Join(t.TempDir(), "codex-profile")
	opts := Options{HomeDir: home, CodexHome: codexHome}

	claude, err := Root(ClientClaude, opts)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".claude", "skills"), claude)

	codex, err := Root(ClientCodex, opts)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(codexHome, "skills"), codex)

	codex, err = Root(ClientCodex, Options{HomeDir: home})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".codex", "skills"), codex)

	codex, err = Root(ClientCodex, Options{HomeDir: home, CodexHome: "~/alternate-codex"})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, "alternate-codex", "skills"), codex)
	codex, err = Root(ClientCodex, Options{HomeDir: home, CodexHome: `~\windows-codex`})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, "windows-codex", "skills"), codex)

	_, err = Root(Client("gemini"), opts)
	require.ErrorContains(t, err, "valid values are claude, codex")
}

func TestInstall_ClientMarkersAreIndependent(t *testing.T) {
	home := t.TempDir()
	opts := Options{HomeDir: home}

	claude, err := Install(ClientClaude, opts)
	require.NoError(t, err)
	require.Equal(t, ActionInstalled, claude.Onboard)
	require.Equal(t, ActionInstalled, claude.Research)
	require.FileExists(t, filepath.Join(claude.Root, OnboardName, "SKILL.md"))
	require.FileExists(t, filepath.Join(claude.Root, OnboardName, "agents", "openai.yaml"))
	require.FileExists(t, filepath.Join(claude.Root, ResearchName, "SKILL.md"))
	require.FileExists(t, filepath.Join(claude.Root, OnboardMarker))
	research := filepath.Join(claude.Root, ResearchName, "SKILL.md")
	researchBefore, err := os.Stat(research)
	require.NoError(t, err)

	// An identical reinstall reports the truth and leaves the managed files in
	// place rather than replacing them with byte-identical copies.
	claudeAgain, err := Install(ClientClaude, opts)
	require.NoError(t, err)
	require.Equal(t, ActionUnchanged, claudeAgain.Onboard)
	require.Equal(t, ActionUnchanged, claudeAgain.Research)
	researchAfter, err := os.Stat(research)
	require.NoError(t, err)
	require.True(t, os.SameFile(researchBefore, researchAfter))

	// Simulate the one-shot skill removing itself after a successful run. An
	// upgrade must honor this client's marker and leave it gone.
	require.NoError(t, os.RemoveAll(filepath.Join(claude.Root, OnboardName)))
	claudeAfterUse, err := Install(ClientClaude, opts)
	require.NoError(t, err)
	require.Equal(t, ActionAlreadyDelivered, claudeAfterUse.Onboard)
	require.NoDirExists(t, filepath.Join(claude.Root, OnboardName))
	require.Equal(t, ActionUnchanged, claudeAfterUse.Research)

	// Claude's delivered marker must not suppress Codex's first delivery.
	codex, err := Install(ClientCodex, opts)
	require.NoError(t, err)
	require.Equal(t, ActionInstalled, codex.Onboard)
	require.FileExists(t, filepath.Join(codex.Root, OnboardName, "SKILL.md"))
	require.NotEqual(t, claude.Root, codex.Root)
}

func TestInstall_RecurringSkillRefreshesAndOptOutsStayLocal(t *testing.T) {
	home := t.TempDir()
	opts := Options{HomeDir: home, SkipOnboard: true}
	result, err := Install(ClientCodex, opts)
	require.NoError(t, err)
	require.Equal(t, ActionSkipped, result.Onboard)
	require.Equal(t, ActionInstalled, result.Research)
	require.NoFileExists(t, filepath.Join(result.Root, OnboardMarker))

	research := filepath.Join(result.Root, ResearchName, "SKILL.md")
	require.NoError(t, files.AtomicWrite(research, []byte("stale"), 0o644))
	result, err = Install(ClientCodex, opts)
	require.NoError(t, err)
	require.Equal(t, ActionUpdated, result.Research)
	content, err := os.ReadFile(research)
	require.NoError(t, err)
	require.Contains(t, string(content), "name: seam-research")
	require.NotEqual(t, "stale", string(content))

	result, err = Install(ClientCodex, Options{HomeDir: home, SkipOnboard: true, SkipResearch: true})
	require.NoError(t, err)
	require.Equal(t, ActionSkipped, result.Onboard)
	require.Equal(t, ActionSkipped, result.Research)
}

// A skill whose optional feature is off must not be installed, and a copy left
// from when the feature was on must go: the whole point is that agents never
// read about tools the server no longer exposes. Re-enabling restores it.
func TestInstall_DisabledSkillIsRemovedAndReturnsWhenEnabled(t *testing.T) {
	home := t.TempDir()
	enabled := Options{HomeDir: home}
	disabled := Options{HomeDir: home, DisabledSkills: []string{ResearchName}}

	result, err := Install(ClientClaude, enabled)
	require.NoError(t, err)
	require.Equal(t, ActionInstalled, result.Research)
	require.FileExists(t, filepath.Join(result.Root, ResearchName, "SKILL.md"))

	result, err = Install(ClientClaude, disabled)
	require.NoError(t, err)
	require.Equal(t, ActionRemoved, result.Research)
	require.NoDirExists(t, filepath.Join(result.Root, ResearchName))
	// Only the one package goes: the onboarding skill and its marker are a
	// different concern and stay exactly where they were.
	require.Equal(t, ActionUnchanged, result.Onboard)
	require.DirExists(t, filepath.Join(result.Root, OnboardName))
	require.FileExists(t, filepath.Join(result.Root, OnboardMarker))

	// Steady state reports the truth rather than a second "removed".
	result, err = Install(ClientClaude, disabled)
	require.NoError(t, err)
	require.Equal(t, ActionDisabled, result.Research)
	require.NoDirExists(t, filepath.Join(result.Root, ResearchName))

	result, err = Install(ClientClaude, enabled)
	require.NoError(t, err)
	require.Equal(t, ActionInstalled, result.Research)
	require.FileExists(t, filepath.Join(result.Root, ResearchName, "SKILL.md"))
}

// The explicit opt-out means "Seamless does not manage this package", which
// outranks the feature state in both directions: nothing is installed, and
// nothing is deleted.
func TestInstall_ExplicitOptOutOutranksTheFeatureState(t *testing.T) {
	home := t.TempDir()
	installed, err := Install(ClientCodex, Options{HomeDir: home})
	require.NoError(t, err)
	require.Equal(t, ActionInstalled, installed.Research)

	result, err := Install(ClientCodex, Options{
		HomeDir:        home,
		SkipResearch:   true,
		DisabledSkills: []string{ResearchName},
	})
	require.NoError(t, err)
	require.Equal(t, ActionSkipped, result.Research)
	require.FileExists(t, filepath.Join(result.Root, ResearchName, "SKILL.md"))

	// And with the feature ON, the env escape hatch still forces a skip.
	result, err = Install(ClientCodex, Options{HomeDir: home, SkipResearch: true})
	require.NoError(t, err)
	require.Equal(t, ActionSkipped, result.Research)
	require.FileExists(t, filepath.Join(result.Root, ResearchName, "SKILL.md"))
}

func TestInstalled_ReportsPackagePresencePerClient(t *testing.T) {
	home := t.TempDir()
	opts := Options{HomeDir: home}

	path, present, err := Installed(ClientClaude, opts, ResearchName)
	require.NoError(t, err)
	require.False(t, present)
	require.Equal(t, filepath.Join(home, ".claude", "skills", ResearchName), path)

	_, err = Install(ClientClaude, opts)
	require.NoError(t, err)
	_, present, err = Installed(ClientClaude, opts, ResearchName)
	require.NoError(t, err)
	require.True(t, present)

	// Claude's copy says nothing about Codex's root.
	_, present, err = Installed(ClientCodex, opts, ResearchName)
	require.NoError(t, err)
	require.False(t, present)

	_, _, err = Installed(Client("gemini"), opts, ResearchName)
	require.ErrorContains(t, err, "valid values are claude, codex")
}

func TestRemove_DryRunAndSelectedClientIsolation(t *testing.T) {
	home := t.TempDir()
	opts := Options{HomeDir: home}
	claude, err := Install(ClientClaude, opts)
	require.NoError(t, err)
	codex, err := Install(ClientCodex, opts)
	require.NoError(t, err)

	preview, err := Remove(ClientCodex, opts, true)
	require.NoError(t, err)
	require.Equal(t, []string{OnboardName, ResearchName}, preview.Skills)
	require.True(t, preview.Marker)
	require.DirExists(t, filepath.Join(codex.Root, OnboardName))
	require.FileExists(t, filepath.Join(codex.Root, OnboardMarker))

	removed, err := Remove(ClientCodex, opts, false)
	require.NoError(t, err)
	require.Equal(t, preview, removed)
	require.NoDirExists(t, filepath.Join(codex.Root, OnboardName))
	require.NoDirExists(t, filepath.Join(codex.Root, ResearchName))
	require.NoFileExists(t, filepath.Join(codex.Root, OnboardMarker))

	// Removing Codex must not touch the coexisting Claude packages or marker.
	require.DirExists(t, filepath.Join(claude.Root, OnboardName))
	require.DirExists(t, filepath.Join(claude.Root, ResearchName))
	require.FileExists(t, filepath.Join(claude.Root, OnboardMarker))
}

func TestAssets_ArePortableCodexSkills(t *testing.T) {
	for _, name := range []string{OnboardName, ResearchName} {
		body, err := assets.ReadFile(filepath.ToSlash(filepath.Join("assets", name, "SKILL.md")))
		require.NoError(t, err)
		text := string(body)
		require.True(t, strings.HasPrefix(text, "---\nname: "+name+"\ndescription:"))
		require.NotContains(t, text, "user-invocable:")
		require.Contains(t, text, "$"+name)
		require.Contains(t, text, "/"+name)
		if name == OnboardName {
			// The onboarding one-shot mutates global instructions and then
			// self-removes, so Claude Code must never auto-invoke it. Codex
			// ignores the unknown key (verified against codex-cli 0.144.6)
			// and keeps its own guard in agents/openai.yaml.
			require.Contains(t, text, "disable-model-invocation: true")
			require.Contains(t, text, "AGENTS.md")
			require.Contains(t, text, "CLAUDE.md")
		} else {
			require.NotContains(t, text, "disable-model-invocation:")
		}
	}
}
