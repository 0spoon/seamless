package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/bench"
)

func TestParseAgentResult(t *testing.T) {
	const object = `{"type":"result","subtype":"success","is_error":false,"duration_ms":4200,
	  "num_turns":5,"session_id":"abc","total_cost_usd":0.4242,
	  "usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":5,"cache_read_input_tokens":10}}`

	tests := []struct {
		name    string
		in      string
		want    bench.Metrics
		session string
		wantErr string
	}{
		{
			name:    "result object",
			in:      object,
			want:    bench.Metrics{Turns: 5, InputTokens: 115, OutputTokens: 50, CostUSD: 0.4242, DurationMS: 4200},
			session: "abc",
		},
		{
			name:    "message array uses the last result",
			in:      `[{"type":"system"},{"type":"assistant"},` + object + `]`,
			want:    bench.Metrics{Turns: 5, InputTokens: 115, OutputTokens: 50, CostUSD: 0.4242, DurationMS: 4200},
			session: "abc",
		},
		{
			name: "older cost field still counts",
			in:   `{"type":"result","num_turns":2,"cost_usd":0.5,"usage":{"input_tokens":7,"output_tokens":3}}`,
			want: bench.Metrics{Turns: 2, InputTokens: 7, OutputTokens: 3, CostUSD: 0.5},
		},
		{
			name: "missing usage is zero, not an error",
			in:   `{"type":"result","num_turns":1}`,
			want: bench.Metrics{Turns: 1},
		},
		{
			name:    "no output at all",
			in:      "   \n",
			wantErr: "no output",
		},
		{
			name:    "not json",
			in:      "claude: command not found",
			wantErr: "parse agent output",
		},
		{
			name:    "array without a result message",
			in:      `[{"type":"system"}]`,
			wantErr: "no result message",
		},
		{
			name:    "a non-result object is not a result",
			in:      `{"type":"assistant"}`,
			wantErr: "want a result",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := parseAgentResult([]byte(tt.in))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, res.metrics())
			require.Equal(t, tt.session, res.SessionID)
		})
	}
}

func TestAgentOpts_Argv(t *testing.T) {
	o := agentOpts{command: "claude", permissionMode: "bypassPermissions", extra: []string{"--verbose"}}
	require.Equal(t,
		[]string{"-p", "continue where we left off", "--output-format", "json", "--permission-mode", "bypassPermissions", "--verbose"},
		o.argv("continue where we left off"))

	bare := agentOpts{command: "claude"}
	require.Equal(t, []string{"-p", "go", "--output-format", "json"}, bare.argv("go"))
}

// testArm builds a minimal arm for the agent-level tests: a repo to run in and
// a config dir to write a transcript into, no daemon and no git.
func testArm(t *testing.T) *arm {
	t.Helper()
	dir := t.TempDir()
	a := &arm{
		condition: bench.Condition{Name: "vanilla", Profile: bench.ProfileVanilla, Client: bench.ClientClaude},
		dir:       dir,
		configEnv: "CLAUDE_CONFIG_DIR",
		configDir: filepath.Join(dir, "home", ".claude"),
		home:      filepath.Join(dir, "home"),
		repo:      filepath.Join(dir, "myapp"),
	}
	require.NoError(t, os.MkdirAll(a.configDir, 0o755))
	require.NoError(t, os.MkdirAll(a.repo, 0o755))
	return a
}

func TestRunAgent_Success(t *testing.T) {
	a := testArm(t)
	scripts := t.TempDir()
	logDir := t.TempDir()
	t.Setenv("FAKE_AGENT_ARGS", filepath.Join(logDir, "args"))
	t.Setenv("FAKE_AGENT_ENVLOG", filepath.Join(logDir, "env"))

	out := runAgent(context.Background(), a, "continue where we left off",
		agentOpts{command: writeScript(t, scripts, "agent.sh", fakeAgentBody), permissionMode: "bypassPermissions"},
		filepath.Join(logDir, bench.AgentLogFile))

	require.NoError(t, out.err)
	require.Equal(t, 0, out.exitCode)
	require.Equal(t, "fake-session", out.sessionID)
	require.Equal(t, bench.Metrics{Turns: 5, InputTokens: 115, OutputTokens: 50, CostUSD: 0.4242, DurationMS: 4200}, out.metrics)

	args, err := os.ReadFile(filepath.Join(logDir, "args"))
	require.NoError(t, err)
	require.Contains(t, string(args), "-p continue where we left off --output-format json --permission-mode bypassPermissions")

	// agent.log carries both streams.
	log, err := os.ReadFile(filepath.Join(logDir, bench.AgentLogFile))
	require.NoError(t, err)
	require.Contains(t, string(log), `"type":"result"`)
	require.Contains(t, string(log), "fake agent stderr")
}

func TestRunAgent_NonZeroExitIsARecordedFailure(t *testing.T) {
	a := testArm(t)
	scripts := t.TempDir()
	logDir := t.TempDir()
	script := writeScript(t, scripts, "boom.sh", "#!/usr/bin/env bash\necho 'kaboom' >&2\nexit 3\n")

	out := runAgent(context.Background(), a, "go", agentOpts{command: script}, filepath.Join(logDir, bench.AgentLogFile))

	require.ErrorContains(t, out.err, "agent exited 3")
	require.Equal(t, 3, out.exitCode)
	require.Greater(t, out.metrics.DurationMS, int64(-1)) // wall clock stands in for the missing result
	log, err := os.ReadFile(filepath.Join(logDir, bench.AgentLogFile))
	require.NoError(t, err)
	require.Contains(t, string(log), "kaboom")
}

func TestRunAgent_TimeoutIsARecordedFailure(t *testing.T) {
	a := testArm(t)
	scripts := t.TempDir()
	logDir := t.TempDir()
	// exec, so the killed process is the sleeper itself: a forked child would
	// hold the output pipe open until WaitDelay expires, which is the runner's
	// backstop for a real agent's subprocesses but only slows this test down.
	script := writeScript(t, scripts, "slow.sh", "#!/usr/bin/env bash\nexec sleep 30\n")

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	out := runAgent(ctx, a, "go", agentOpts{command: script}, filepath.Join(logDir, bench.AgentLogFile))

	require.ErrorContains(t, out.err, "timed out")
	require.NotEqual(t, 0, out.exitCode)
}

func TestRunAgent_MissingBinaryIsARecordedFailure(t *testing.T) {
	a := testArm(t)
	logDir := t.TempDir()
	out := runAgent(context.Background(), a, "go",
		agentOpts{command: filepath.Join(t.TempDir(), "not-installed")}, filepath.Join(logDir, bench.AgentLogFile))

	require.ErrorContains(t, out.err, "run agent")
	require.Equal(t, -1, out.exitCode)
}
