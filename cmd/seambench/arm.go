// One built condition arm, as described by the env file
// scripts/fixture/harness.sh --mode bench writes for it. The env file is the
// whole interface between the shell harness and this runner: the harness owns
// building the arm, this file owns reading it back, and neither hardcodes the
// other's paths.
//
// The client-keyed config variable is deliberately read by NAME
// (SEAMBENCH_CLIENT_CONFIG_ENV names it -- CLAUDE_CONFIG_DIR for claude,
// CODEX_HOME for codex) rather than assumed, so adding a client changes one
// table entry in the harness instead of this runner.

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/0spoon/seamless/internal/bench"
)

// arm is one condition arm the runner can seed, serve, and run an agent in.
type arm struct {
	condition bench.Condition
	model     string // the agent model the harness pinned in the arm's settings.json
	dir       string // the arm's own directory (holds env, config, key, logs)
	envFile   string
	configEnv string // name of the client-keyed config var (e.g. CLAUDE_CONFIG_DIR)
	configDir string // its value: the arm's agent config home
	home      string // the arm's throwaway HOME
	repo      string // the arm's demo-repo (myapp) copy
	snapshot  string // demo-repo commit captured after the harness built the arm

	// Seamless-ful arms only (a vanilla arm has none of this).
	seamless bool
	config   string // SEAMLESS_CONFIG: the arm's throwaway daemon config
	port     string
	url      string
	dataDir  string
	keyFile  string
}

// envVarRE matches the shell variable names the harness emits.
var envVarRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseEnvFile parses the harness's shell-sourceable KEY="value" lines. Values
// are emitted unescaped (they are filesystem paths and slugs), so unwrapping a
// matching pair of surrounding quotes is the whole grammar. A line that is
// neither blank, a comment, nor an assignment is an error rather than a skip:
// the env file is generated, so anything else means the two halves have drifted.
func parseEnvFile(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		k, v, ok := strings.Cut(text, "=")
		k = strings.TrimSpace(k)
		if !ok || !envVarRE.MatchString(k) {
			return nil, fmt.Errorf("line %d: not a KEY=\"value\" assignment: %q", line, text)
		}
		out[k] = unquote(strings.TrimSpace(v))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read env: %w", err)
	}
	return out, nil
}

// unquote strips one matching pair of surrounding quotes.
func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// armEnvPath is where the harness writes an arm's env file.
func armEnvPath(base, condition string) string {
	return filepath.Join(base, "arms", condition, "env")
}

// loadArm reads one arm's env file and checks it describes the condition the
// runner asked the harness to build. A mismatch is fatal rather than adaptive:
// the two condition parsers (bench.ParseConditions here, the shell parser
// there) implement the same grammar on purpose, so a disagreement means one of
// them changed and the arm on disk is not the arm being measured.
func loadArm(path string, cond bench.Condition) (*arm, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open arm env for condition %s: %w", cond.Name, err)
	}
	defer func() { _ = f.Close() }()

	vars, err := parseEnvFile(f)
	if err != nil {
		return nil, fmt.Errorf("parse arm env %s: %w", path, err)
	}

	a := &arm{
		condition: cond,
		dir:       filepath.Dir(path),
		envFile:   path,
		model:     vars["SEAMBENCH_MODEL"],
	}
	for _, req := range []struct {
		key string
		dst *string
	}{
		{"SEAMBENCH_CLIENT_CONFIG_ENV", &a.configEnv},
		{"SEAMBENCH_HOME", &a.home},
		{"SEAMBENCH_MYAPP", &a.repo},
	} {
		v := vars[req.key]
		if v == "" {
			return nil, fmt.Errorf("arm env %s: missing %s", path, req.key)
		}
		*req.dst = v
	}
	if a.configDir = vars[a.configEnv]; a.configDir == "" {
		return nil, fmt.Errorf("arm env %s: missing %s (named by SEAMBENCH_CLIENT_CONFIG_ENV)", path, a.configEnv)
	}

	for _, want := range []struct {
		key, value string
	}{
		{"SEAMBENCH_CONDITION", cond.Name},
		{"SEAMBENCH_PROFILE", string(cond.Profile)},
		{"SEAMBENCH_CLIENT", string(cond.Client)},
	} {
		if got := vars[want.key]; got != want.value {
			return nil, fmt.Errorf("arm env %s: %s is %q, want %q -- the built arm does not match the requested condition", path, want.key, got, want.value)
		}
	}

	switch vars["SEAMBENCH_SEAMLESS"] {
	case "0":
		a.seamless = false
	case "1":
		a.seamless = true
	default:
		return nil, fmt.Errorf("arm env %s: SEAMBENCH_SEAMLESS is %q, want 0 or 1", path, vars["SEAMBENCH_SEAMLESS"])
	}
	if !a.seamless {
		return a, nil
	}
	for _, req := range []struct {
		key string
		dst *string
	}{
		{"SEAMLESS_CONFIG", &a.config},
		{"SEAMBENCH_PORT", &a.port},
		{"SEAMBENCH_URL", &a.url},
		{"SEAMBENCH_DATA_DIR", &a.dataDir},
		{"SEAMBENCH_KEY_FILE", &a.keyFile},
	} {
		v := vars[req.key]
		if v == "" {
			return nil, fmt.Errorf("arm env %s: missing %s on a Seamless-ful arm", path, req.key)
		}
		*req.dst = v
	}
	return a, nil
}

// key reads the arm's throwaway API key (the hook and MCP bearer).
func (a *arm) key() (string, error) {
	b, err := os.ReadFile(a.keyFile)
	if err != nil {
		return "", fmt.Errorf("read arm key %s: %w", a.keyFile, err)
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", fmt.Errorf("arm key file %s is empty", a.keyFile)
	}
	return key, nil
}

// agentEnv is the environment the headless agent runs under. The inherited
// environment is kept (the agent needs its own credentials and PATH) minus
// every variable that could point it at live state -- HOME, either client's
// config home, and the whole SEAMLESS_/SEAMBENCH_ space -- which are then set
// back to this arm's throwaway values. A vanilla arm gets no SEAMLESS_CONFIG at
// all, so a stray one in the parent shell cannot quietly wire the control arm.
func (a *arm) agentEnv() []string {
	env := scrubEnv(os.Environ())
	env = append(env,
		"HOME="+a.home,
		a.configEnv+"="+a.configDir,
	)
	if a.seamless {
		env = append(env, "SEAMLESS_CONFIG="+a.config)
	}
	return env
}

// daemonEnv is the environment the arm's seamlessd runs under: its own config
// and its own home, nothing inherited that could resolve a live path.
func (a *arm) daemonEnv() []string {
	return append(scrubEnv(os.Environ()),
		"HOME="+a.home,
		"SEAMLESS_CONFIG="+a.config,
	)
}

// scrubEnv drops the variables an arm must own.
func scrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "HOME", k == "CLAUDE_CONFIG_DIR", k == "CODEX_HOME":
			continue
		case strings.HasPrefix(k, "SEAMLESS_"), strings.HasPrefix(k, "SEAMBENCH_"):
			continue
		}
		out = append(out, kv)
	}
	return out
}
