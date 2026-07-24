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
	"For non-trivial work, read injected briefing/recall context before searching again. Use session_start/session_end when an explicit handoff record is useful. If this session caused any mishaps (an action a warning said not to take, live state touched by mistake), self-report them in session_end's mishaps field -- recurrence review is how they get fixed. Claim shared plan steps with tasks_claim before working them. When writing a memory, " + KindDiscriminator + "."

// KindDiscriminator is the constraint-vs-convention call an agent makes at
// memory_write time. The memory_write kind parameter and MCPInstructions both
// render this string so the write-side judgment cannot drift between surfaces.
const KindDiscriminator = "constraint = what any agent must or must not do regardless of task; convention = a project-local choice or layout fact (naming, branding, where things live or deploy, which files sync together)"

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
		"mishaps",
		"constraint",
		"convention",
	}
}

// RequiredToolNames is the subset of the guidance contract that must resolve
// to real MCP tools. Catalog tests keep prose from naming a stale workflow.
func RequiredToolNames() []string {
	return []string{"recall", "memory_write", "notes_create", "session_start", "session_end", "tasks_claim"}
}
