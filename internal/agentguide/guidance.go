// Package agentguide owns the concise, cross-surface guidance that teaches an
// agent how Seamless's tools compose. Keeping these strings below the MCP and
// retrieval surfaces prevents their shared workflow from drifting into two
// independently maintained summaries.
package agentguide

// MCPInstructions is returned in the MCP initialize result. Codex consumes this
// as server-wide guidance, including while it is deciding whether to expose or
// call an individual tool. Keep the first paragraph self-contained and under
// 512 characters; current Codex clients prioritize that prefix.
const MCPInstructions = "Use Seamless as durable context. Recall before guessing: call recall before asking users to re-find knowledge. memory_write stores compact durable knowledge; notes_create stores long artifacts. Do not duplicate code, AGENTS.md/CLAUDE.md, or this conversation. If scope is ambiguous, pass project explicitly; project=global is only for cross-project knowledge. Use sessions/findings for handoff. Compose plans as notes + tasks with plan:<slug>. Trust each tool's inputSchema required fields and enums over prose.\n\n" +
	"For non-trivial work, read injected briefing/recall context before searching again. Use session_start/session_end when an explicit handoff record is useful. Claim shared plan steps with tasks_claim before working them."

// BriefingFooter is the compact reminder appended to every ambient briefing.
// It deliberately names only the two retrieval entry points; MCPInstructions
// carries the fuller workflow once a client initializes the tool server.
const BriefingFooter = "Recall on demand with recall; read a memory with memory_read. Trust a tool's inputSchema (required + enums) over prose when building a call.\n"

// RequiredWorkflowTerms is the canonical cross-surface contract used by tests
// for MCP instructions and the maintained onboarding skill. Return a fresh
// slice so callers cannot mutate shared process state.
func RequiredWorkflowTerms() []string {
	return []string{
		"recall",
		"memory_write",
		"notes_create",
		"AGENTS.md",
		"CLAUDE.md",
		"project=global",
		"sessions",
		"findings",
		"plan:<slug>",
		"tasks_claim",
		"inputSchema",
	}
}

// RequiredToolNames is the subset of the guidance contract that must resolve
// to real MCP tools. Catalog tests keep prose from naming a stale workflow.
func RequiredToolNames() []string {
	return []string{"recall", "memory_write", "notes_create", "session_start", "session_end", "tasks_claim"}
}
