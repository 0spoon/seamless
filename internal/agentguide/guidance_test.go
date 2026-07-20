package agentguide

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestMCPInstructions_FirstParagraphIsSelfContained(t *testing.T) {
	first, _, _ := strings.Cut(MCPInstructions, "\n")
	require.LessOrEqual(t, utf8.RuneCountInString(first), 512)

	for _, concept := range []string{
		"Recall before guessing",
		"memory_write",
		"notes_create",
		"AGENTS.md/CLAUDE.md",
		"project explicitly",
		"sessions/findings",
		"plan:<slug>",
		"inputSchema",
	} {
		require.Contains(t, first, concept)
	}
}

func TestBriefingFooter_PreservesRetrievalAndSchemaReminders(t *testing.T) {
	require.Contains(t, BriefingFooter, "recall")
	require.Contains(t, BriefingFooter, "memory_read")
	require.Contains(t, BriefingFooter, "inputSchema")
}

func TestMCPInstructions_ContainsCanonicalWorkflowTerms(t *testing.T) {
	for _, term := range RequiredWorkflowTerms() {
		require.Contains(t, MCPInstructions, term)
	}
	for _, tool := range RequiredToolNames() {
		require.Contains(t, MCPInstructions, tool)
	}
}
