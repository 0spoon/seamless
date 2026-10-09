package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	agentskills "github.com/arctop/seamless/internal/skills"
	"github.com/stretchr/testify/require"
)

// publishedKnobs is installerKnobs as first published. The list may grow, but
// this prefix never changes: a knob renamed, dropped or reordered fails here
// instead of in an update nobody is watching. Extend installerKnobs, never this.
var publishedKnobs = []string{
	"SEAMLESS_VERSION",
	"SEAMLESS_INSTALL_DIR",
	"SEAMLESS_CHECKSUMS_SHA256",
	"SEAMLESS_CLIENT",
	"SEAMLESS_NO_HOOKS",
	"SEAMLESS_NO_ONBOARD_SKILL",
	"SEAMLESS_NO_RESEARCH_SKILL",
}

// inheritedKnobs are the knobs neither installer reads itself: `seamlessd
// install-hooks` reads them, from the environment the installer runs it in
// (skills.OptionsFromEnvironment). The installer's whole job is to leave them
// alone. Each maps to the install-hooks option it switches on.
var inheritedKnobs = map[string]func(agentskills.Options) bool{
	"SEAMLESS_NO_ONBOARD_SKILL":  func(o agentskills.Options) bool { return o.SkipOnboard },
	"SEAMLESS_NO_RESEARCH_SKILL": func(o agentskills.Options) bool { return o.SkipResearch },
}

// installerCode returns an installer's code lines: everything but comments, so
// a knob named only in the header's Overrides list counts as documented, not
// handled. It is line-level -- a trailing comment on a code line reads as code
// -- which is fine for two scripts that carry no such comment naming a knob.
func installerCode(t *testing.T, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", name))
	require.NoError(t, err)
	var code []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			code = append(code, line)
		}
	}
	return code
}

func linesMatching(lines []string, re *regexp.Regexp) []string {
	var out []string
	for _, line := range lines {
		if re.MatchString(line) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

func TestInstallerKnobs_AppendOnly(t *testing.T) {
	require.GreaterOrEqual(t, len(installerKnobs), len(publishedKnobs))
	require.Equal(t, publishedKnobs, installerKnobs[:len(publishedKnobs)],
		"installerKnobs is append-only: add new knobs at the end and never rename or drop one")
	seen := map[string]bool{}
	for _, knob := range installerKnobs {
		require.Regexp(t, `^SEAMLESS_[A-Z0-9_]+$`, knob)
		require.False(t, seen[knob], "%s is listed twice", knob)
		seen[knob] = true
	}
	for knob := range inheritedKnobs {
		require.True(t, seen[knob], "inheritedKnobs names %s, which is not an updater knob", knob)
	}
}

// TestInstallerKnobs_BothInstallersHandleEach holds docs/install and
// docs/install.ps1 to every knob the updater passes. A knob the installer
// reads must be READ in code in both: a `${NAME` expansion in sh -- the only
// read `set -u` allows on an unset variable -- and `$env:NAME` in PowerShell.
// A knob install-hooks reads must not be touched by either script's code at all
// (no read, unset, override or removal), so it reaches install-hooks as the
// updater set it.
func TestInstallerKnobs_BothInstallersHandleEach(t *testing.T) {
	scripts := []struct {
		name string
		read func(knob string) *regexp.Regexp
	}{
		{"install", func(knob string) *regexp.Regexp { return regexp.MustCompile(`\$\{` + knob + `\b`) }},
		{"install.ps1", func(knob string) *regexp.Regexp { return regexp.MustCompile(`\$env:` + knob + `\b`) }},
	}
	for _, script := range scripts {
		code := installerCode(t, script.name)
		for _, knob := range installerKnobs {
			t.Run(script.name+"/"+knob, func(t *testing.T) {
				if _, inherited := inheritedKnobs[knob]; inherited {
					require.Empty(t, linesMatching(code, regexp.MustCompile(`\b`+knob+`\b`)),
						"docs/%s touches %s in code, but install-hooks is what reads it; if the installer now handles it, take it out of inheritedKnobs",
						script.name, knob)
					require.NotEmpty(t, linesMatching(code, regexp.MustCompile(`install-hooks`)),
						"docs/%s no longer runs install-hooks, which is what reads %s", script.name, knob)
					return
				}
				require.NotEmpty(t, linesMatching(code, script.read(knob)),
					"docs/%s never reads %s outside a comment; the updater passes it, so the installer must handle it",
					script.name, knob)
			})
		}
	}
}

// TestInstallerKnobs_InheritedReachInstallHooks is the other half of an
// inherited knob's contract: install-hooks, which both installers run, honours
// it from its environment.
func TestInstallerKnobs_InheritedReachInstallHooks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	knobs := make([]string, 0, len(inheritedKnobs))
	for knob := range inheritedKnobs {
		knobs = append(knobs, knob)
	}
	slices.Sort(knobs)
	for _, knob := range knobs {
		option := inheritedKnobs[knob]
		t.Run(knob, func(t *testing.T) {
			t.Setenv(knob, "")
			off, err := agentskills.OptionsFromEnvironment()
			require.NoError(t, err)
			require.False(t, option(off))

			t.Setenv(knob, "1")
			on, err := agentskills.OptionsFromEnvironment()
			require.NoError(t, err)
			require.True(t, option(on), "install-hooks no longer honours %s", knob)
		})
	}
}
