package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeTranscript(t *testing.T, lines string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(lines), 0o600))
	return path
}

func TestHarvestFindingsLastAssistantMessage(t *testing.T) {
	path := writeTranscript(t, strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"start"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"First answer"},{"type":"tool_use","id":"a","name":"Bash","input":{}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Final summary of the work."}]}}`,
		``, // trailing blank line tolerated
	}, "\n"))

	got := harvestFindings(path)
	require.Equal(t, "(auto-harvested) Final summary of the work.", got)
}

func TestHarvestFindingsContentAsString(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"assistant","message":{"role":"assistant","content":"Plain string answer"}}`)
	require.Equal(t, "(auto-harvested) Plain string answer", harvestFindings(path))
}

func TestHarvestFindingsConcatenatesTextBlocks(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]}}`)
	require.Equal(t, "(auto-harvested) line one\nline two", harvestFindings(path))
}

func TestHarvestFindingsFallbacks(t *testing.T) {
	require.Equal(t, harvestFallback, harvestFindings(""), "blank path")
	require.Equal(t, harvestFallback, harvestFindings("/no/such/file.jsonl"), "missing file")

	// A transcript with no assistant text falls back too.
	path := writeTranscript(t, strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Bash"}]}}`,
		`not even json`,
	}, "\n"))
	require.Equal(t, harvestFallback, harvestFindings(path))
}

func TestHarvestFindingsCapsLength(t *testing.T) {
	long := strings.Repeat("x", 5000)
	path := writeTranscript(t,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"`+long+`"}]}}`)
	got := harvestFindings(path)
	body := strings.TrimPrefix(got, "(auto-harvested) ")
	require.Len(t, []rune(body), maxHarvestRunes)
}

func TestInjectAmbientLine(t *testing.T) {
	// Into an existing briefing: line goes just before the closing tag.
	brief := "<seam-briefing>\nSeam project: demo\n</seam-briefing>"
	got := injectAmbientLine(brief, "cc/abc12345")
	require.Contains(t, got, "Seam session: cc/abc12345 (ambient)")
	require.True(t, strings.HasSuffix(got, "(ambient)\n</seam-briefing>"))

	// No briefing: a minimal wrapper is produced.
	got = injectAmbientLine("", "cc/abc12345")
	require.Equal(t, "<seam-briefing>\nSeam session: cc/abc12345 (ambient)\n</seam-briefing>", got)
}

func TestAmbientName(t *testing.T) {
	cases := []struct {
		name   string
		client Client
		id     string
		want   string
	}{
		{"claude-code readable prefix plus digest", ClientClaudeCode, "ABC12345-6789-0000", "cc/abc12345-537a9f0416b71992"},
		{"claude-code short id plus digest", ClientClaudeCode, "short", "cc/short-f9b0078b5df596d2"},
		{"codex readable prefix plus digest", ClientCodex, "ABC12345-6789-0000", "cx/abc12345-537a9f0416b71992"},
		{"codex short id plus digest", ClientCodex, "short", "cx/short-f9b0078b5df596d2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ambientName(tc.client, tc.id))
		})
	}
}
