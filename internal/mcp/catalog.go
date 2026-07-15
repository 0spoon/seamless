package mcp

import "github.com/mark3labs/mcp-go/mcp"

// Catalog returns every registered tool's definition, in registration order.
//
// It exists so documentation tooling (cmd/docsgen) can render the tool surface
// without constructing a Server: the constructors are plain data -- name,
// description, and input schema -- and need no DB, config, or listening port.
// The definitions here are the same values registerTools hands to addTool, so
// the docs describe exactly what the server serves.
//
// The order below MUST mirror registerTools. catalog_test enforces both halves
// of that contract (length against ToolCount, names against a live server's
// registration order), so a tool added to one and not the other fails the build
// rather than silently vanishing from the docs.
func Catalog() []mcp.Tool {
	return []mcp.Tool{
		sessionStartTool(),
		sessionUpdateTool(),
		sessionEndTool(),

		memoryWriteTool(),
		memoryAppendTool(),
		memoryReadTool(),
		memoryDeleteTool(),

		recallTool(),

		notesCreateTool(),
		notesReadTool(),
		notesUpdateTool(),
		notesAppendTool(),
		notesDeleteTool(),

		projectListTool(),
		projectCreateTool(),

		tasksAddTool(),
		tasksUpdateTool(),
		tasksReadyTool(),
		tasksListTool(),
		tasksClaimTool(),
		tasksReleaseTool(),

		labOpenTool(),
		trialRecordTool(),
		trialQueryTool(),

		gardenerProposalsTool(),
		gardenerRequestTool(),
		gardenerSplitTool(),
		gardenerApplyTool(),

		captureURLTool(),
		usageSummaryTool(),
	}
}
