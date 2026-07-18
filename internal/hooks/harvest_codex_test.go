package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// rolloutPath is the committed Codex rollout fixture (a real full-turn session
// file, trimmed and path-sanitized -- see testdata/codex/README).
func rolloutPath() string { return filepath.Join("testdata", "codex", "rollout.jsonl") }

// The sentinel final answer the fixture turn produced, present in all three
// agent-message shapes (task_complete, agent_message, assistant response_item).
const rolloutFinalAnswer = "SEAMLESS_SENTINEL_SESSIONSTART=zebra-4417\nSEAMLESS_SENTINEL_PROMPTSUBMIT=falcon-9928"

// The whole tail-parse acceptance: the real rollout fixture yields the turn's
// last agent message.
func TestTailCodexRollout_Fixture(t *testing.T) {
	require.Equal(t, rolloutFinalAnswer, tailCodexRollout(rolloutPath()))
}

// Each of the three line shapes that carry the final answer decodes; everything
// else (input messages, reasoning, token counts, malformed lines) decodes to "".
func TestCodexRolloutLineMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want string
	}{
		{
			"task_complete carries last_agent_message",
			`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done"}}`,
			"done",
		},
		{
			"agent_message carries message",
			`{"type":"event_msg","payload":{"type":"agent_message","message":"answer","phase":"final_answer"}}`,
			"answer",
		},
		{
			"assistant response_item concatenates output_text",
			`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"},{"type":"output_text","text":"b"}]}}`,
			"a\nb",
		},
		{
			"user response_item is not harvested",
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`,
			"",
		},
		{
			"developer response_item is not harvested",
			`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"SEAMLESS_SENTINEL_X=1"}]}}`,
			"",
		},
		{"unrelated event_msg is empty", `{"type":"event_msg","payload":{"type":"token_count"}}`, ""},
		{"reasoning item is empty", `{"type":"response_item","payload":{"type":"reasoning"}}`, ""},
		{"malformed line is empty", `{not json`, ""},
		{"blank line is empty", ``, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, codexRolloutLineMessage([]byte(tc.line)))
		})
	}
}

// The last matching line wins, so multiple turns in one file converge on the most
// recent final answer.
func TestTailCodexRollout_LastMatchWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	body := strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"turn one"}}`,
		`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"turn two"}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	require.Equal(t, "turn two", tailCodexRollout(path))
}

// A rollout larger than the tail window is read from the end: the final answer is
// found and the truncated leading partial line is dropped, not misparsed.
func TestTailCodexRollout_TailWindowDropsLeadingPartial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	// A first line larger than the whole tail window, so the read seeks into its
	// middle and must discard the partial remainder.
	pad := strings.Repeat("x", maxRolloutTailBytes+50_000)
	body := `{"type":"session_meta","payload":{"note":"` + pad + `"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"tail answer"}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	require.Equal(t, "tail answer", tailCodexRollout(path))
}

// A blank or missing/moved transcript path degrades to "" -- the Stop hook then
// heartbeats only, never errors.
func TestTailCodexRollout_MissingOrBlank(t *testing.T) {
	require.Empty(t, tailCodexRollout(""))
	require.Empty(t, tailCodexRollout("   "))
	require.Empty(t, tailCodexRollout(filepath.Join(t.TempDir(), "does-not-exist.jsonl")))
}

// codexStopFindings prefers the Stop payload's last_assistant_message (the common
// path in codex-cli 0.144.5) and formats it like the CC harvest.
func TestCodexStopFindings_PrefersPayload(t *testing.T) {
	got := codexStopFindings("  the answer  ", rolloutPath())
	require.Equal(t, "(auto-harvested) the answer", got,
		"payload wins over the rollout and is trimmed + prefixed")
}

// With no payload message, it falls back to tail-parsing the rollout file.
func TestCodexStopFindings_FallsBackToRollout(t *testing.T) {
	got := codexStopFindings("", rolloutPath())
	require.Equal(t, "(auto-harvested) "+rolloutFinalAnswer, got)
}

// Nothing to harvest -> "" so the caller leaves any prior turn's findings intact.
func TestCodexStopFindings_EmptyWhenNothing(t *testing.T) {
	require.Empty(t, codexStopFindings("", ""))
	require.Empty(t, codexStopFindings("   ", filepath.Join(t.TempDir(), "missing.jsonl")))
}

// A final answer longer than the harvest cap is truncated, matching the CC path.
func TestCodexStopFindings_CapsLongMessage(t *testing.T) {
	long := strings.Repeat("z", maxHarvestRunes+500)
	got := codexStopFindings(long, "")
	require.Equal(t, "(auto-harvested) "+strings.Repeat("z", maxHarvestRunes), got)
}
