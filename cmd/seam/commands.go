package main

// The command table: ordering only.
//
// Every spec lives next to its handler (var captureCmd = spec(...) above
// runCapture), so one-file-per-command survives and a command's contract sits
// where its code does. This file decides only what order the reader meets them
// in, which is also the order help renders.
//
// The invariant that keeps the migration honest while legacyDispatch still
// exists: a command enters this table exactly when its handler converts. It is
// never declared in two places, so there is no window in which the table and the
// heredoc can disagree.

// Group headings, in help order. Declared as constants rather than written at
// each spec so a typo cannot silently open a second section.
const (
	groupAgentLoop     = "agent loop"
	groupTasks         = "tasks"
	groupPlans         = "captured plans (Claude Code plan mode)"
	groupObservability = "observability"
	groupHooks         = "hooks (invoked by Claude Code, not by hand)"
)

// groupOrder is the order help renders the sections in. A group with no migrated
// commands renders nothing, which is what lets the table and the shrinking
// heredoc coexist without either knowing about the other.
var groupOrder = []string{groupAgentLoop, groupTasks, groupPlans, groupObservability, groupHooks}

// commands returns the migrated command table, in help order.
//
// A function rather than a package-level var: each spec closes over a bind that
// registers flags on a fresh FlagSet per parse, and a var would invite callers to
// share one table across parses when the point is that binding is per-call.
func commands() []cmd {
	return []cmd{
		primeCmd,
		rememberCmd,
		recallCmd,
		captureCmd,

		readyCmd,
		taskListCmd,
		taskAddCmd,
		taskDoneCmd,
		taskStartCmd,
		taskDropCmd,
		taskReopenCmd,
		taskClaimCmd,
		taskHeartbeatCmd,
		taskReleaseCmd,

		planListCmd,
		planShowCmd,
		planCheckCmd,
		planApproveCmd,
	}
}
