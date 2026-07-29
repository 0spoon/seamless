package mcp

import "github.com/mark3labs/mcp-go/mcp"

// Tool behaviour hints, one explicit set per tool.
//
// Saying nothing is not neutral. mcp-go leaves Tool.Annotations zero when a
// constructor passes no annotation, and the MCP spec's defaults for the absent
// fields are the pessimistic ones -- readOnlyHint false, destructiveHint true.
// So "no opinion" reaches the client as "may destroy things", and every tool
// here used to serve exactly that. gardener_proposals is the case that made it
// visible: a single SELECT whose description ends "Read-only." was advertising
// destructiveHint true, contradicting itself on the wire.
//
// Where the destructive line falls follows the project's own storage split (see
// CLAUDE.md): the markdown files are the source of truth for durable knowledge,
// and the DB is the record for high-churn state. Destructive therefore marks
// the calls that delete or overwrite a memory or a note. Reassigning a field on
// a session, task, trial, or favorite is a state transition, not destruction --
// those records exist to be reassigned, and the same tool moves them back.
//
// Idempotent is read as the spec words it: repeating the call with the same
// arguments has no further effect. That separates the creators and appenders
// (each call adds another thing) from the setters (the second call lands the
// same state).
//
// Open-world is false everywhere but capture_url, the only tool whose domain is
// the internet. An LLM call behind recall or the gardener is an implementation
// detail over a local corpus, not an unbounded domain.
//
// These are functions rather than package-level vars so each tool gets its own
// *bool set; a shared annotation would hand every tool pointers into the same
// four booleans.

// hintRead marks a tool that only reads: recall, the readers, the listers.
func hintRead() mcp.ToolOption { return toolHints(true, false, true, false) }

// hintAdd marks an additive write whose repetition accumulates -- a create, an
// append, a claim that refreshes its lease.
func hintAdd() mcp.ToolOption { return toolHints(false, false, false, false) }

// hintSet marks a state transition that settles: calling it twice with the same
// arguments leaves the same state.
func hintSet() mcp.ToolOption { return toolHints(false, false, true, false) }

// hintOverwrite marks the calls that can remove or replace durable content --
// a memory or note deleted, rewritten in place, or superseded.
func hintOverwrite() mcp.ToolOption { return toolHints(false, true, true, false) }

// hintFetch marks capture_url, the one tool that reaches the open internet.
func hintFetch() mcp.ToolOption { return toolHints(false, false, false, true) }

// toolHints builds the annotation from the four spec hints. Every field is set
// explicitly, including the two the spec calls meaningful only when readOnly is
// false: a client that reads them without checking readOnly first still sees a
// consistent answer rather than the pessimistic default.
func toolHints(readOnly, destructive, idempotent, openWorld bool) mcp.ToolOption {
	return mcp.WithToolAnnotation(mcp.ToolAnnotation{
		ReadOnlyHint:    &readOnly,
		DestructiveHint: &destructive,
		IdempotentHint:  &idempotent,
		OpenWorldHint:   &openWorld,
	})
}
