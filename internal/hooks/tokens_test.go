package hooks

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestClaudeTranscriptTokensDedupsByMessageID is the core Claude Code rule: a
// single assistant message is written as several JSONL lines that share one
// message.id and REPEAT the same usage block, so each id's usage must count once.
// Summing every line would multiply the true total.
func TestClaudeTranscriptTokensDedupsByMessageID(t *testing.T) {
	// msg_A: 3 lines, identical usage repeated (in=100 out=50 read=1000 create=200).
	// msg_B: 1 line (in=10 out=5 read=0 create=0). Only unique ids contribute.
	path := writeTranscript(t, strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"go"}}`,
		aLine("msg_A", 100, 50, 1000, 200),
		aLine("msg_A", 100, 50, 1000, 200),
		aLine("msg_A", 100, 50, 1000, 200),
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`,
		aLine("msg_B", 10, 5, 0, 0),
		``,
	}, "\n"))

	got, ok := claudeTranscriptTokens(path)
	require.True(t, ok)
	require.Equal(t, 110, got.Input, "100 + 10, each id once")
	require.Equal(t, 55, got.Output)
	require.Equal(t, 1000, got.Cached)
	require.Equal(t, 200, got.CacheCreation)
	require.Equal(t, 110+55+1000+200, got.Total)
}

// TestClaudeTranscriptTokensIncludesSidechain: real token spend counts subagent
// (isSidechain) turns against the launching session, unlike model attribution
// which skips them.
func TestClaudeTranscriptTokensIncludesSidechain(t *testing.T) {
	path := writeTranscript(t, strings.Join([]string{
		aLine("msg_main", 100, 50, 0, 0),
		`{"type":"assistant","isSidechain":true,"message":{"id":"msg_sub","role":"assistant","usage":{"input_tokens":7,"output_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
	}, "\n"))

	got, ok := claudeTranscriptTokens(path)
	require.True(t, ok)
	require.Equal(t, 107, got.Input)
	require.Equal(t, 53, got.Output)
}

// TestClaudeTranscriptTokensIdempotentOnGrownFile is the resume/double-count rule
// at the parser level: because the totals are absolute (the store OVERWRITES), a
// re-harvest of a grown transcript yields the new absolute total, never the sum of
// two harvests. Re-parsing the first snapshot is stable; parsing the grown file
// gives the new cumulative figure.
func TestClaudeTranscriptTokensIdempotentOnGrownFile(t *testing.T) {
	first := strings.Join([]string{
		aLine("msg_A", 100, 50, 0, 0),
	}, "\n")
	path := writeTranscript(t, first)

	got1, ok := claudeTranscriptTokens(path)
	require.True(t, ok)
	require.Equal(t, 150, got1.Total)

	// Re-parsing the same file is stable (no accumulation in the parser).
	got1b, _ := claudeTranscriptTokens(path)
	require.Equal(t, got1, got1b, "parser is stateless -- same file, same total")

	// The session resumes and the transcript grows (msg_A is retained, msg_B added).
	grown := strings.Join([]string{
		aLine("msg_A", 100, 50, 0, 0),
		aLine("msg_B", 20, 10, 0, 0),
	}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(grown), 0o600))

	got2, ok := claudeTranscriptTokens(path)
	require.True(t, ok)
	require.Equal(t, 180, got2.Total, "absolute cumulative total of the grown file, not 150+180")
}

func TestClaudeTranscriptTokensFallbacks(t *testing.T) {
	_, ok := claudeTranscriptTokens("")
	require.False(t, ok, "blank path")
	_, ok = claudeTranscriptTokens("/no/such/file.jsonl")
	require.False(t, ok, "missing file")

	// No assistant usage -> not ok.
	path := writeTranscript(t, strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","message":{"id":"msg_x","role":"assistant","content":[{"type":"text","text":"no usage block"}]}}`,
		`not even json`,
	}, "\n"))
	got, ok := claudeTranscriptTokens(path)
	require.False(t, ok)
	require.True(t, got.Empty())
}

// TestCodexRolloutTokensLastCumulativeWins: Codex emits a cumulative token_count
// every turn; the last one is the whole-session figure. input_tokens INCLUDES the
// cached portion, so the normalized fresh input is input - cached.
func TestCodexRolloutTokensLastCumulativeWins(t *testing.T) {
	path := writeRollout(t, strings.Join([]string{
		tokenCountLine(5000, 4000, 30), // turn 1 (cumulative)
		`{"type":"event_msg","payload":{"type":"agent_message","message":"hi"}}`,
		tokenCountLine(12655, 9600, 75), // turn 2 (cumulative, later -> wins)
	}, "\n"))

	got, ok := codexRolloutTokens(path)
	require.True(t, ok)
	require.Equal(t, 12655-9600, got.Input, "fresh input = total input - cached")
	require.Equal(t, 9600, got.Cached)
	require.Equal(t, 0, got.CacheCreation, "Codex has no cache-creation notion")
	require.Equal(t, 75, got.Output)
	require.Equal(t, 12730, got.Total, "matches Codex's own total_tokens (input + output)")
}

func TestCodexRolloutTokensFallbacks(t *testing.T) {
	_, ok := codexRolloutTokens("")
	require.False(t, ok, "blank path")
	_, ok = codexRolloutTokens("/no/such/rollout.jsonl")
	require.False(t, ok, "missing file")

	// A rollout with no token_count event -> not ok.
	path := writeRollout(t, strings.Join([]string{
		`{"type":"session_meta","payload":{"source":"exec"}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"hi"}}`,
	}, "\n"))
	got, ok := codexRolloutTokens(path)
	require.False(t, ok)
	require.True(t, got.Empty())
}

// TestCodexRolloutTokensMatchesVersionedFixture parses the committed live-captured
// rollout so a real Codex payload shape stays covered.
func TestCodexRolloutTokensMatchesVersionedFixture(t *testing.T) {
	path := filepath.Join("testdata", "codex", "v0.144.5", "rollout.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	got, ok := codexRolloutTokens(path)
	require.True(t, ok)
	require.Equal(t, 12655-9600, got.Input)
	require.Equal(t, 9600, got.Cached)
	require.Equal(t, 75, got.Output)
	require.Equal(t, 12730, got.Total)
}

// aLine builds one Claude Code assistant transcript line with a message id and a
// usage block.
func aLine(id string, in, out, cacheRead, cacheCreate int) string {
	return `{"type":"assistant","message":{"id":"` + id + `","role":"assistant","content":[{"type":"text","text":"x"}],"usage":{` +
		`"input_tokens":` + strconv.Itoa(in) + `,"output_tokens":` + strconv.Itoa(out) +
		`,"cache_read_input_tokens":` + strconv.Itoa(cacheRead) +
		`,"cache_creation_input_tokens":` + strconv.Itoa(cacheCreate) + `}}}`
}

// tokenCountLine builds one Codex rollout token_count event with a cumulative
// total_token_usage (input includes the cached portion, as Codex reports it).
func tokenCountLine(input, cached, output int) string {
	return `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{` +
		`"input_tokens":` + strconv.Itoa(input) + `,"cached_input_tokens":` + strconv.Itoa(cached) +
		`,"output_tokens":` + strconv.Itoa(output) + `,"total_tokens":` + strconv.Itoa(input+output) + `}}}}`
}

func writeRollout(t *testing.T, lines string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(lines), 0o600))
	return path
}
