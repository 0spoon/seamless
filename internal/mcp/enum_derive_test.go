package mcp

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

// asStrings widens a canonical set declared over a named string type, so the
// expectation below is the set itself rather than a second transcription of it.
func asStrings[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

// TestArgsEnumsDeriveFromCanonicalSets is the anti-drift gate: every enum a tool
// advertises must be its canonical set, exactly and in order.
//
// It is same-package (like catalog_test) because it reads the built schema, and
// it exists because transcribing a set is a silent failure with a live example:
// tools_gardener hand-listed 6 of store.ProposalKinds' 7 kinds, so abandon_plan
// -- which the migration, the gardener, the applier, and the console all handle
// -- was undiscoverable at the boundary for months. Nothing failed; the schema
// simply lied. Now a 9th MemoryKind (or an 8th proposal kind) fails the build
// unless it reaches the schema too.
//
// Since validateMiddleware enforces the declared enum, this also pins that the
// values enforced at the boundary are the ones the handlers' Valid() checks
// accept: a transcription slip would otherwise reject a value the store takes,
// which is exactly how enforcing the hand-written list would have regressed
// abandon_plan.
func TestArgsEnumsDeriveFromCanonicalSets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tool  func() mcp.Tool
		param string
		want  []string
	}{
		{"memory_write.kind", memoryWriteTool, "kind", asStrings(core.MemoryKinds)},
		{"tasks_update.status", tasksUpdateTool, "status", asStrings(core.TaskStatuses)},
		{"tasks_list.status", tasksListTool, "status", asStrings(core.TaskStatuses)},
		{"gardener_proposals.kind", gardenerProposalsTool, "kind", store.ProposalKinds},
		{"recall.scope", recallTool, "scope", retrieve.RecallScopes},
		{"session_start.source", sessionStartTool, "source", core.SessionSources},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prop, err := propSchema(tc.tool().InputSchema, tc.param)
			require.NoError(t, err)

			got, ok, err := enumValues(prop, tc.param)
			require.NoError(t, err)
			require.True(t, ok, "%s: declares no enum -- an undeclared enum is not enforced", tc.name)
			require.Equal(t, tc.want, got, "%s: the schema's enum must BE the canonical set, not a copy of it", tc.name)
		})
	}
}
