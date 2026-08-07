package features

// Guard for the shared fixture's dependence on an optional feature.
//
// Reading a repo file from a package test follows the precedent in
// internal/hooks/codex_contract_test.go, which pins the Codex hook profile
// against docs-src the same way. It adds no import to this leaf package: the
// registry stays config-only.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// harnessPath is the shared fixture entry point, relative to this package.
var harnessPath = filepath.Join("..", "..", "scripts", "fixture", "harness.sh")

// harnessSection slices one marker-delimited span out of the harness script,
// failing loudly when a marker is gone so a rename cannot silently turn an
// assertion into a no-op. An empty end marker takes everything after start.
func harnessSection(t *testing.T, script, start, end string) string {
	t.Helper()
	_, after, found := strings.Cut(script, start)
	require.True(t, found,
		"%s: %q is gone -- this guard scans for it, so move the scan with the rename", harnessPath, start)
	if end == "" {
		return after
	}
	section, _, found := strings.Cut(after, end)
	require.True(t, found, "%s: %q no longer follows %q", harnessPath, end, start)
	return section
}

// Invariant 7: the fixture harness turns the research feature on, in BOTH modes,
// through BOTH mechanisms.
//
// A fixture instance is a NEW installation: optional features ship off and there
// are no pre-existing trials for the grandfather migration to notice. Both halves
// of the fixture need the feature anyway -- seambench grades on trial_query being
// called and trial.recorded being emitted, and the branding scenes seed a failed
// trial and screenshot the Labs/Trials screens -- so the harness enables it
// explicitly, and neither mechanism is redundant:
//
//  1. `features: research: true` in each throwaway seamless.yaml, which is what
//     reaches a daemon started later by cmd/seambench's arm runner (arm.go's
//     scrubEnv wipes the whole SEAMLESS_* space and sets back only
//     SEAMLESS_CONFIG) or by the operator in another terminal during a record
//     take.
//  2. The exported env var, which covers the harness's own children --
//     install-hooks, the demoseed seeder, the self-check daemon -- whichever
//     config each of them resolves.
//
// The comment block at the top of harness.sh is the long form of this. Deleting
// either mechanism silently produces a fixture whose agent cannot record a trial,
// which surfaces as an unexplained benchmark regression, so it fails here first.
func TestFixtureHarness_EnablesResearchBothWays(t *testing.T) {
	raw, err := os.ReadFile(harnessPath)
	require.NoError(t, err)
	script := string(raw)

	// Derived, not transcribed: config binds SEAMLESS_FEATURES_<KEY> for each
	// optional feature, and the YAML key is the registry key itself.
	envVar := "SEAMLESS_FEATURES_" + strings.ToUpper(string(Research))
	yamlKey := string(Research) + ": true"

	// Mechanism 2, and its placement: an unindented export in the script prologue,
	// before either mode's entry point, so it applies to --mode record and --mode
	// bench alike rather than to one branch.
	// require.True rather than require.Contains: the failure message is the whole
	// point of this guard, and Contains would bury it under the entire script.
	exportLine := "\nexport " + envVar + "=1\n"
	require.True(t, strings.Contains(script, exportLine),
		"%s must `export %s=1` at the top level: the harness's own children (install-hooks, the "+
			"demoseed seeder, the self-check daemon) pick the feature up from the environment. "+
			"See the %s comment block at the top of the script for why this is not redundant with "+
			"the config file.", harnessPath, envVar, envVar)

	exportAt := strings.Index(script, exportLine)
	for _, entry := range []string{"run_record() {", "run_bench() {"} {
		at := strings.Index(script, entry)
		require.NotEqual(t, -1, at,
			"%s: %s is gone -- this guard checks the export precedes both mode entry points, so the "+
				"scan needs to follow the rename", harnessPath, entry)
		require.Less(t, exportAt, at,
			"%s: `export %s=1` must stay in the unconditional prologue, ahead of %s -- both modes "+
				"need the feature on, so it must not move inside one mode's branch",
			harnessPath, envVar, entry)
	}

	// Mechanism 1: the throwaway config every seeded instance is started against.
	// This is the load-bearing one -- see the comment block above.
	cfg := harnessSection(t, script, "write_config() {", "\nEOF")
	require.Contains(t, cfg, "features:",
		"%s: write_config must write a `features:` block into each throwaway seamless.yaml -- the "+
			"seambench arm runner scrubs the SEAMLESS_* environment, so the exported variable alone "+
			"never reaches the daemon", harnessPath)
	require.Contains(t, cfg, yamlKey,
		"%s: write_config's seamless.yaml must set `%s` under `features:` -- a fixture instance is a "+
			"new installation, so without it the arm's daemon serves no research tools and every "+
			"trial-based grader reports a regression that is really a fixture bug", harnessPath, yamlKey)

	// Both modes go through write_config, so mechanism 1 covers both.
	record := harnessSection(t, script, "run_record() {", "run_bench() {")
	require.Contains(t, record, "write_config",
		"%s: --mode record must seed its instance through write_config, or the recorded scenes run "+
			"against a daemon with the Labs/Trials screens switched off", harnessPath)
	bench := harnessSection(t, script, "run_bench() {", "")
	require.Contains(t, bench, "write_config",
		"%s: --mode bench must build each Seamless-ful arm through write_config, or the benchmark "+
			"grades arms whose agents cannot call the research tools", harnessPath)

	// The rationale stays next to the mechanisms: the export and the write_config
	// note both name the variable, so removing one leaves the other pointing at it.
	require.GreaterOrEqual(t, strings.Count(script, envVar), 2,
		"%s must keep the comment that explains why the export and the config block BOTH exist -- "+
			"a future editor who deletes one as duplication is exactly who this guard is for",
		harnessPath)
}
