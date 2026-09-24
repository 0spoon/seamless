package mcp

import (
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// Optimistic concurrency for the mutating tools: the caller passes the
// content_hash it last read, and the write is refused if the file has moved on.
//
// Two different races, two different answers, and this is only one of them.
// files.Manager.Mutate serializes application writes, so two agents can no
// longer interleave a read and a write; what it cannot see is an agent acting on
// knowledge it gathered minutes ago, or the owner editing the markdown in an
// editor. That is what expect_hash catches -- which is why it is checked against
// the FILE's current hash inside the mutation lock, never against the index row:
// the watcher re-indexes on a 300ms debounce, so an owner's save is invisible to
// the index for a window in which the index would happily report the stale hash
// the agent is holding, and the precondition would pass on exactly the edit it
// exists to stop.
//
// The parameter is optional everywhere. A caller that omits it gets the previous
// behavior (last write wins, now at least serialized), which keeps every
// existing agent working; a caller that sends it gets a refusal instead of a
// silent overwrite.

// expectHashParamDescription is the shared prose, so every tool advertising the
// precondition describes the same contract.
const expectHashParamDescription = "optional precondition: the content_hash you last read for this item " +
	"(memory_read/notes_read return it). The write is refused if the stored file has changed since -- " +
	"another agent or the owner edited it -- so re-read and re-apply your change instead of overwriting theirs. " +
	"Omit it to write unconditionally."

// withExpectHashParam declares the optional expect_hash parameter on a tool.
func withExpectHashParam() mcp.ToolOption {
	return mcp.WithString("expect_hash", mcp.Description(expectHashParamDescription))
}

// checkExpectHash enforces the precondition when the caller sent one. current
// must be the hash of the file as just read under the mutation lock (every
// files.Store read populates ContentHash from the bytes it parsed).
//
// The mismatch is a plain error, not a payload field: an agent that treats a
// success response as success would carry on believing its edit landed. The
// message names both hashes so a retry loop can tell "someone else moved it" from
// "I sent the wrong hash".
func checkExpectHash(req mcp.CallToolRequest, tool, current string) error {
	want := normalizeHash(argString(req, "expect_hash"))
	if want == "" {
		return nil
	}
	if got := normalizeHash(current); want != got {
		return fmt.Errorf("%s: expect_hash %s does not match the stored content hash %s -- "+
			"the item changed since you read it; read it again and re-apply your change",
			tool, want, got)
	}
	return nil
}

// normalizeHash trims and lowercases a hex digest so a caller echoing back an
// upper-case or padded hash is not told its correct precondition failed.
func normalizeHash(h string) string {
	return strings.ToLower(strings.TrimSpace(h))
}
