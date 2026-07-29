// Running the agent headless and reading its result.
//
// The agent command is injectable (--agent-cmd) rather than hardwired to
// `claude`. That is the seam a dry run or a test drives a fake agent through --
// a real take costs real tokens, so the whole runner must be exercisable
// without one -- and it is also where a second client's CLI would plug in.
//
// Headless `claude -p` fires SessionStart but NOT UserPromptSubmit, so a
// scenario whose signal is the mid-session <seam-recall> injection cannot be
// measured this way at all (memory headless-cc-p-skips-userpromptsubmit-hook).
// Those scenarios take the component-check path in recall.go instead; this file
// only ever runs the briefing-surfaced ones.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/0spoon/seamless/internal/bench"
)

// agentOpts is the agent invocation, minus the per-run parts.
type agentOpts struct {
	// command is the agent CLI (default "claude").
	command string
	// permissionMode is the value for --permission-mode; empty omits the flag.
	// A headless run in a throwaway repo has no human to approve edits, so the
	// default bypasses -- everything it can reach was built by the harness.
	permissionMode string
	// extra arguments appended verbatim, for a flag this runner does not model.
	extra []string
	// env is appended to the arm's environment as KEY=VALUE, for credentials
	// that authenticate the agent without a file on disk (see credentials.go).
	// It holds secrets, so it is never logged.
	env []string
}

// runOutcome is what one attempt at a run produced, before capture.
type runOutcome struct {
	metrics   bench.Metrics
	exitCode  int
	sessionID string // the agent's own session id, used to find its transcript
	// err is set when the RUN failed -- a timeout, a non-zero exit, an
	// unreadable result -- as opposed to the agent merely doing the wrong
	// thing, which is the grader's business.
	err error
}

// argv builds the headless invocation for one prompt.
func (o agentOpts) argv(prompt string) []string {
	args := []string{"-p", prompt, "--output-format", "json"}
	if o.permissionMode != "" {
		args = append(args, "--permission-mode", o.permissionMode)
	}
	return append(args, o.extra...)
}

// runAgent runs the agent in the arm's demo repo under the arm's environment,
// tees its output to the run's agent.log, and extracts the runner-owned
// metrics from the result JSON. A timeout or a non-zero exit is a failed run
// recorded in the outcome, never a crash of the suite: the caller still
// captures the artifacts and moves to the next run.
func runAgent(ctx context.Context, a *arm, prompt string, o agentOpts, logPath string) runOutcome {
	logFile, err := os.Create(logPath)
	if err != nil {
		return runOutcome{exitCode: -1, err: fmt.Errorf("create agent log %s: %w", logPath, err)}
	}
	defer func() { _ = logFile.Close() }()

	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, o.command, o.argv(prompt)...)
	cmd.Dir = a.repo
	cmd.Env = append(a.agentEnv(), o.env...)
	cmd.Stdout = io.MultiWriter(&stdout, logFile)
	cmd.Stderr = logFile
	cmd.WaitDelay = 10 * time.Second

	started := time.Now()
	runErr := cmd.Run()
	wall := time.Since(started)

	// ProcessState is nil when the command never started (a missing binary), so
	// -1 stands for "no exit status", the same value ExitCode reports for a
	// signalled process.
	out := runOutcome{exitCode: -1}
	if cmd.ProcessState != nil {
		out.exitCode = cmd.ProcessState.ExitCode()
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			out.err = fmt.Errorf("agent timed out")
		case errors.As(runErr, &exitErr):
			out.err = fmt.Errorf("agent exited %d", exitErr.ExitCode())
		default:
			out.err = fmt.Errorf("run agent %s: %w", o.command, runErr)
		}
	}

	res, parseErr := parseAgentResult(stdout.Bytes())
	if parseErr != nil {
		if out.err == nil {
			out.err = fmt.Errorf("read agent result (see %s): %w", logPath, parseErr)
		}
		out.metrics.DurationMS = wall.Milliseconds()
		return out
	}
	out.sessionID = res.SessionID
	out.metrics = res.metrics()
	if out.metrics.DurationMS == 0 {
		out.metrics.DurationMS = wall.Milliseconds()
	}
	if out.err == nil && res.IsError {
		// The process succeeded but the agent reported failure (max turns,
		// execution error). The artifacts are still captured; the run is
		// recorded as failed so the report does not average it in as a verdict.
		out.err = fmt.Errorf("agent reported an error result (%s)", res.Subtype)
	}
	return out
}

// agentResult is the `--output-format json` result message. Fields the runner
// does not measure are left out; the shape is read tolerantly (both the object
// and the transcript-array form, and both the current total_cost_usd and the
// older cost_usd) because it belongs to the agent CLI, not to this repo.
type agentResult struct {
	Type       string  `json:"type"`
	Subtype    string  `json:"subtype"`
	IsError    bool    `json:"is_error"`
	DurationMS int64   `json:"duration_ms"`
	NumTurns   int     `json:"num_turns"`
	SessionID  string  `json:"session_id"`
	TotalCost  float64 `json:"total_cost_usd"`
	LegacyCost float64 `json:"cost_usd"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// metrics maps the result onto the runner-owned half of bench.Metrics. The
// grader owns the rest and derives it from the preserved artifacts.
//
// InputTokens is the FULL input volume the run consumed -- fresh, cache-write,
// and cache-read tokens summed -- because that is what a run costs and what a
// version-over-version comparison is about. Splitting it would need fields the
// frozen contract does not have.
func (r agentResult) metrics() bench.Metrics {
	cost := r.TotalCost
	if cost == 0 {
		cost = r.LegacyCost
	}
	return bench.Metrics{
		Turns:        r.NumTurns,
		InputTokens:  r.Usage.InputTokens + r.Usage.CacheCreationInputTokens + r.Usage.CacheReadInputTokens,
		OutputTokens: r.Usage.OutputTokens,
		CostUSD:      cost,
		DurationMS:   r.DurationMS,
	}
}

// parseAgentResult extracts the result message from the agent's stdout. It
// accepts a bare result object and an array of messages ending in one, since
// the two output shapes have both shipped.
func parseAgentResult(b []byte) (agentResult, error) {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 {
		return agentResult{}, errors.New("agent produced no output")
	}
	if trimmed[0] == '[' {
		var msgs []agentResult
		if err := json.Unmarshal(trimmed, &msgs); err != nil {
			return agentResult{}, fmt.Errorf("parse agent output array: %w", err)
		}
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Type == "result" {
				return msgs[i], nil
			}
		}
		return agentResult{}, errors.New("agent output array has no result message")
	}
	var res agentResult
	if err := json.Unmarshal(trimmed, &res); err != nil {
		return agentResult{}, fmt.Errorf("parse agent output: %w", err)
	}
	if res.Type != "" && res.Type != "result" {
		return agentResult{}, fmt.Errorf("agent output is a %q message, want a result", res.Type)
	}
	return res, nil
}
