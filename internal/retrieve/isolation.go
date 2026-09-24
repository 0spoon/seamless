package retrieve

import (
	"context"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// Project isolation as the retrieval surfaces see it. Every decision here
// routes through store.CanRead, whose matrix is tested once beside the store,
// so a fence added on this side cannot drift from the policy it enforces. What
// lives here is only the plumbing: which query a narrowed scope runs, which
// family members survive the fence, and the header line that tells an agent
// its briefing was assembled under one.

// globalReadable reports whether a session bound to project may read the
// global scope. Only a sealed project may not. Every retrieval surface that
// unions project+global asks here first, so the inbound fence cannot hold in
// recall while leaking through the briefing, the prompt corpus, link
// expansion, or a dedup hint.
func (s *Service) globalReadable(ctx context.Context, project string) (bool, error) {
	return store.CanRead(ctx, s.db, project, "")
}

// scopedActiveMemories loads the active memories an agent bound to project may
// see: its own, global, and any extra slugs the caller widened the scope with
// (a shared parent). A sealed project reads nothing outside itself, so the
// global scope drops out and the query narrows to the named projects alone. It
// backs the briefing index, recall's browse mode, and the prompt matcher's
// corpus, so none of them can pick up knowledge the others refuse.
func (s *Service) scopedActiveMemories(ctx context.Context, project string, extra []string) ([]core.Memory, error) {
	global, err := s.globalReadable(ctx, project)
	if err != nil {
		return nil, err
	}
	if global {
		return store.ActiveMemoriesForScope(ctx, s.db, project, extra)
	}
	return store.ActiveMemoriesForProjects(ctx, s.db, append([]string{project}, extra...))
}

// familyPeers filters a project's family members (its shared parent, its
// siblings) down to those it may exchange briefing content with. Family
// cross-over is bidirectional -- a member both contributes findings and
// memories and receives them -- so it requires the read fence open BOTH ways:
// an isolated member never leaks into this briefing, and an isolated caller
// receives nothing from its family either. Isolation already requires a
// standalone project, so in practice this fires only on topology rows that
// outlived a tighten; it is the defense in depth the design asks for, not the
// primary guard.
func (s *Service) familyPeers(ctx context.Context, project string, peers []string) ([]string, error) {
	kept := make([]string, 0, len(peers))
	for _, peer := range peers {
		// pull: the peer's knowledge entering this briefing.
		pull, err := store.CanRead(ctx, s.db, project, peer)
		if err != nil {
			return nil, err
		}
		if !pull {
			continue
		}
		// push: this project's knowledge reaching a briefing assembled for the
		// peer. A confidential caller passes the pull check (its inbound side is
		// open) and must still be dropped here.
		push, err := store.CanRead(ctx, s.db, peer, project)
		if err != nil {
			return nil, err
		}
		if push {
			kept = append(kept, peer)
		}
	}
	if len(kept) == 0 {
		return nil, nil
	}
	return kept, nil
}

// isolationLine renders the isolation header for a fenced project. Both
// assemblers write it directly under their header line -- a subagent is an
// agent inside the fence too, and its scope is narrowed before it renders.
// An agent inside a sealed project must know its briefing is deliberately
// missing the global memories and family sections it would otherwise carry,
// and one inside a confidential project must know a write aimed at another
// scope will be refused. An open project gets no line -- the default state is
// not news.
//
// Neither sanitizer applies: the line interpolates no stored text at all. The
// state is a fixed enum rendered as a literal, and the project slug already
// renders verbatim through sanitizeName in the header line above it
// (briefing-scrub-prose-only-names-verbatim: the scrub is for prose, never for
// identifiers).
func isolationLine(iso core.Isolation) string {
	switch iso {
	case core.IsolationSealed:
		return "Isolation: sealed -- this briefing contains only this project's knowledge (no global memories, no family)\n"
	case core.IsolationConfidential:
		return "Isolation: confidential -- writes outside this project are disabled\n"
	}
	// Open, and any state store.CanRead also reads as open: no line rather than
	// a promise the fences are not making.
	return ""
}
