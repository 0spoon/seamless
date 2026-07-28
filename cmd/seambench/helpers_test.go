// Shared fixtures for the runner tests: a fake condition arm laid out exactly
// the way scripts/fixture/harness.sh --mode bench lays one out, and a fake
// agent CLI that behaves like `claude -p --output-format json` without costing
// a token. Everything here is hermetic -- no harness script, no daemon, no real
// agent -- so `make test` never depends on any of the three.

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/bench"
)

// armFixture is one fake arm on disk.
type armFixture struct {
	cond      bench.Condition
	seamless  bool
	dir       string
	home      string
	configDir string
	repo      string
	dataDir   string
	config    string
	keyFile   string
	envFile   string
	head      string // the demo repo's initial commit
}

const fixtureKey = "0123456789abcdef0123456789abcdef"
const fixtureModel = "claude-opus-5"

// newArmFixture builds one arm under base, mirroring the harness's layout and
// env file. The demo repo is a real git repo so the snapshot/reset/diff path is
// exercised for real.
func newArmFixture(t *testing.T, base string, cond bench.Condition, port int) armFixture {
	t.Helper()
	requireGit(t)

	a := armFixture{
		cond:     cond,
		seamless: cond.Profile != bench.ProfileVanilla,
		dir:      filepath.Join(base, "arms", cond.Name),
	}
	a.home = filepath.Join(a.dir, "home")
	a.configDir = filepath.Join(a.home, ".claude")
	a.repo = filepath.Join(a.dir, "myapp")
	a.dataDir = filepath.Join(a.dir, "data")
	a.config = filepath.Join(a.dir, "seamless.yaml")
	a.keyFile = filepath.Join(a.dir, "key.txt")
	a.envFile = filepath.Join(a.dir, "env")

	require.NoError(t, os.MkdirAll(a.configDir, 0o755))
	a.head = initRepo(t, a.repo)

	lines := []string{
		`# arm "` + cond.Name + `" -- test fixture`,
		`SEAMBENCH_CONDITION="` + cond.Name + `"`,
		`SEAMBENCH_PROFILE="` + string(cond.Profile) + `"`,
		`SEAMBENCH_CLIENT="` + string(cond.Client) + `"`,
		`SEAMBENCH_MODEL="` + fixtureModel + `"`,
		`SEAMBENCH_CLIENT_CONFIG_ENV="CLAUDE_CONFIG_DIR"`,
		`CLAUDE_CONFIG_DIR="` + a.configDir + `"`,
		`SEAMBENCH_HOME="` + a.home + `"`,
		`SEAMBENCH_MYAPP="` + a.repo + `"`,
	}
	if !a.seamless {
		lines = append(lines, `SEAMBENCH_SEAMLESS="0"`)
	} else {
		require.NoError(t, os.WriteFile(a.keyFile, []byte(fixtureKey+"\n"), 0o600))
		require.NoError(t, os.WriteFile(a.config, []byte(fmt.Sprintf(
			"addr: \"127.0.0.1:%d\"\ndata_dir: %q\nmcp:\n  api_key: %q\n", port, a.dataDir, fixtureKey)), 0o644))
		lines = append(lines,
			`SEAMBENCH_SEAMLESS="1"`,
			`SEAMLESS_CONFIG="`+a.config+`"`,
			fmt.Sprintf(`SEAMBENCH_PORT="%d"`, port),
			fmt.Sprintf(`SEAMBENCH_URL="http://127.0.0.1:%d"`, port),
			`SEAMBENCH_DATA_DIR="`+a.dataDir+`"`,
			`SEAMBENCH_KEY_FILE="`+a.keyFile+`"`,
		)
	}
	require.NoError(t, os.WriteFile(a.envFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	return a
}

// requireGit skips a test that cannot run without git.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// initRepo creates a one-commit git repo and returns its HEAD.
func initRepo(t *testing.T, dir string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	}
	run("init", "-q")
	run("config", "user.email", "dev@myapp.local")
	run("config", "user.name", "myapp")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	run("add", "-A")
	run("commit", "-qm", "init")

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

// fakeAgentBody is a stand-in for `claude -p ... --output-format json`: it
// records the argv and environment it was handed, writes a plausible
// transcript into the arm's config home, edits the demo repo (one tracked
// append plus one new file), and prints a result message.
const fakeAgentBody = `#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"$FAKE_AGENT_ARGS"
printf 'cwd=%s home=%s config=%s seamless=%s\n' "$PWD" "$HOME" "${CLAUDE_CONFIG_DIR:-unset}" "${SEAMLESS_CONFIG:-unset}" >>"$FAKE_AGENT_ENVLOG"
mkdir -p "$CLAUDE_CONFIG_DIR/projects/-fixture-myapp"
printf '{"type":"user"}\n{"type":"assistant"}\n' >"$CLAUDE_CONFIG_DIR/projects/-fixture-myapp/fake-session.jsonl"
echo '// touched by the agent' >>main.go
printf 'package main\n\n// a shared-storage limiter\n' >ratelimit.go
echo "fake agent stderr" >&2
cat <<'JSON'
{"type":"result","subtype":"success","is_error":false,"duration_ms":4200,"num_turns":5,"session_id":"fake-session","total_cost_usd":0.4242,"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":5,"cache_read_input_tokens":10}}
JSON
`

// writeScript writes an executable shell script and returns its path.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
	return path
}
