package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Transcript lines in the shapes Claude Code actually writes. The narration
// line is the bug this file guards: it is real assistant text, so the old
// parser stored it as the report whenever the hook read the file before the
// final message landed.
const (
	lnPrompt    = `{"type":"user","message":{"role":"user","content":"Do the thing"}}`
	lnNarration = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Now the console files."}]}}`
	lnToolUse   = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Now the console files."},{"type":"tool_use","name":"Read","input":{}}]}}`
	lnToolResp  = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`
	lnThinking  = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"weighing it up"}]}}`
	lnReport    = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done. Here is the full report."}]}}`
)

func writeAgentTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-atest.jsonl")
	var b []byte
	for _, l := range lines {
		b = append(b, l...)
		b = append(b, '\n')
	}
	require.NoError(t, os.WriteFile(path, b, 0o600))
	return path
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	_, err = f.WriteString(line + "\n")
	require.NoError(t, err)
}

// A transcript is only finished when its LAST line is an assistant message
// carrying text and no tool_use. Every other trailing shape means the agent is
// still working or the writer is mid-flush, and the newest assistant text is
// narration rather than the report.
func TestParseSubagentTranscript_Completeness(t *testing.T) {
	tests := []struct {
		name       string
		lines      []string
		wantReport string
		complete   bool
	}{
		{
			name:     "terminal assistant text",
			lines:    []string{lnPrompt, lnNarration, lnToolResp, lnReport},
			complete: true, wantReport: "Done. Here is the full report.",
		},
		{
			name:     "trailing tool_use beside text is narration",
			lines:    []string{lnPrompt, lnToolUse},
			complete: false, wantReport: "Now the console files.",
		},
		{
			name:     "trailing tool_result",
			lines:    []string{lnPrompt, lnNarration, lnToolResp},
			complete: false, wantReport: "Now the console files.",
		},
		{
			// Claude Code emits the final turn's thinking as its own line before
			// the text line, so a trailing thinking block is the last thing on
			// disk in exactly the window this bug lived in.
			name:     "trailing lone thinking block",
			lines:    []string{lnPrompt, lnNarration, lnThinking},
			complete: false, wantReport: "Now the console files.",
		},
		{
			name:     "half-written trailing line",
			lines:    []string{lnPrompt, lnNarration, `{"type":"assistant","mess`},
			complete: false, wantReport: "Now the console files.",
		},
		{
			name:     "no assistant text at all",
			lines:    []string{lnPrompt, lnToolResp},
			complete: false, wantReport: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prompt, report, complete := parseSubagentTranscript(writeAgentTranscript(t, tc.lines...))
			require.Equal(t, "Do the thing", prompt, "prompt is the first user message")
			require.Equal(t, tc.wantReport, report)
			require.Equal(t, tc.complete, complete)
		})
	}
}

// An earlier terminal-looking line must not mark a transcript finished: the
// flag describes the last line, not the best line.
func TestParseSubagentTranscript_EarlierTerminalLineDoesNotCount(t *testing.T) {
	_, report, complete := parseSubagentTranscript(
		writeAgentTranscript(t, lnPrompt, lnReport, lnToolResp, lnThinking))
	require.False(t, complete)
	require.Equal(t, "Done. Here is the full report.", report, "the newest assistant text is still returned")
}

func TestAwaitSubagentReport_ReturnsImmediatelyWhenSettled(t *testing.T) {
	h := NewHandler(Config{})
	path := writeAgentTranscript(t, lnPrompt, lnNarration, lnToolResp, lnReport)

	start := time.Now()
	prompt, report := h.awaitSubagentReport(context.Background(), path, "atest")
	require.Less(t, time.Since(start), subagentReportSettle, "a settled transcript must not wait")
	require.Equal(t, "Do the thing", prompt)
	require.Equal(t, "Done. Here is the full report.", report)
}

// The regression: SubagentStop fires while Claude Code is still appending the
// final message, so the report arrives after the capture starts reading.
// Whichever side wins the start, the assertion is the same -- the capture must
// end up holding the report and never the narration line before it.
func TestAwaitSubagentReport_PicksUpAReportThatLandsLate(t *testing.T) {
	h := NewHandler(Config{})
	path := writeAgentTranscript(t, lnPrompt, lnNarration, lnToolResp, lnThinking)

	done := make(chan [2]string, 1)
	go func() {
		prompt, report := h.awaitSubagentReport(context.Background(), path, "atest")
		done <- [2]string{prompt, report}
	}()
	appendLine(t, path, lnReport)

	select {
	case got := <-done:
		require.Equal(t, "Do the thing", got[0])
		require.Equal(t, "Done. Here is the full report.", got[1])
	case <-time.After(subagentReportSettle + 2*time.Second):
		t.Fatal("awaitSubagentReport never returned")
	}
}

// Giving up must yield the truest record available rather than nothing: an
// interrupted subagent leaves narration, and that still beats an empty note.
func TestAwaitSubagentReport_CancelledReturnsWhatItHas(t *testing.T) {
	h := NewHandler(Config{})
	path := writeAgentTranscript(t, lnPrompt, lnNarration, lnToolResp, lnThinking)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	prompt, report := h.awaitSubagentReport(ctx, path, "atest")
	require.Less(t, time.Since(start), subagentReportSettle, "a cancelled wait must not burn the budget")
	require.Equal(t, "Do the thing", prompt)
	require.Equal(t, "Now the console files.", report)
}

func TestAwaitSubagentReport_MissingTranscript(t *testing.T) {
	h := NewHandler(Config{})
	start := time.Now()
	prompt, report := h.awaitSubagentReport(context.Background(),
		filepath.Join(t.TempDir(), "absent.jsonl"), "atest")
	require.Less(t, time.Since(start), subagentReportSettle, "there is nothing to wait for")
	require.Empty(t, prompt)
	require.Empty(t, report)
}
