package features

// Judging a live tools/list count against a registered one.
//
// It lives here, and not in either doctor, because BOTH clients ask the
// question -- `seam doctor` and the client-role `seamlessd doctor` -- and the
// answer is arithmetic over this registry and nothing else: how many tools the
// currently disabled features hide, and which of them to name as the reason. A
// second copy would drift the day a feature is added, and it would drift
// SILENTLY, because both copies would go on printing a plausible number.

import (
	"fmt"
	"strings"

	"github.com/0spoon/seamless/internal/config"
)

// ToolCountVerdict judges the tool count a server exposes and renders the
// doctor line that reports it.
//
// registered is the caller's accounting of every tool that SHOULD be registered
// (mcp.ToolCount inside seamlessd; the mirrored expectedTools const in seam,
// which cannot import the mcp package without dragging SQLite into a CLI whose
// job is one HTTP call). live is what tools/list actually returned. feats is the
// server's effective optional-feature state, or nil when it could not be read,
// in which case why carries the reason.
//
// The two numbers are not the same measurement and the line says so: optional
// features ship OFF, so a fresh install EXPOSES fewer tools than it REGISTERS,
// and that is the common case rather than the exception. A check that asserted
// bare equality would fail every default install out of the box.
//
// One thing this arithmetic cannot see, and the reason the detail always prints
// both numbers: on a client, registered comes from the local binary while live
// comes from the server's. A client and server on different versions therefore
// produce a mismatch that is version skew rather than a broken tool gate, and
// only a human reading both numbers can tell which it is.
func ToolCountVerdict(registered, live int, feats *config.Features, why string) (bool, string) {
	// Failure-soft. Without the feature state there is no single expected
	// number, only a range: everything registered, minus everything the optional
	// features could be hiding. Judging against the range keeps one unreadable
	// endpoint from failing an otherwise healthy daemon, and naming the reason
	// keeps the line from claiming a certainty it does not have. Asserting the
	// full count instead would fail every default install whose console did not
	// answer; assuming all features off would pretend to know they are.
	if feats == nil {
		low := registered - len(ToolOwners())
		return live >= low && live <= registered,
			fmt.Sprintf("%d exposed of %d registered, expected %d-%d (feature state unreadable: %s)",
				live, registered, low, registered, why)
	}
	hidden := HiddenTools(*feats)
	if len(hidden) == 0 {
		return live == registered, fmt.Sprintf("%d tools (expected %d)", live, registered)
	}
	want := registered - len(hidden)
	names := disabledToolOwnerNames(*feats)
	if live == want {
		return true, fmt.Sprintf("%d registered, %d exposed (%s disabled)", registered, live, names)
	}
	return false, fmt.Sprintf("%d registered, %d exposed, expected %d with %s disabled",
		registered, live, want, names)
}

// disabledToolOwnerNames lists, in registry order, the disabled features that
// account for a gap between the registered and exposed tool counts. Features
// that own no tools are left out: they explain nothing about this number.
func disabledToolOwnerNames(c config.Features) string {
	var keys []string
	for _, f := range registry {
		if f.Enabled(c) || len(f.Tools) == 0 {
			continue
		}
		keys = append(keys, string(f.Key))
	}
	return strings.Join(keys, ", ")
}
