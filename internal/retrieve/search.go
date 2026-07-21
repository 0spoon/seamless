package retrieve

import (
	"context"
	"time"
)

// searchSourceDepth is how many candidates each leg contributes before fusion.
// It is deliberately deeper than recallSourceDepth: a human search can range
// over every project at once, so the candidate set has to be wide enough that a
// large scope does not crowd out a match that ranks mid-pack in its own project.
const searchSourceDepth = 60

// SearchInput parameterizes a human-facing search over durable knowledge.
//
// Projects nil means every project -- the difference that makes this a separate
// entry point from Recall, which is always bound to one session's scope.
// Semantic false runs the FTS leg only, for a caller that fires a query per
// keystroke and must not pay a remote embedding round-trip each time.
type SearchInput struct {
	Query    string
	Scope    string    // all | memories | notes
	Projects []string  // nil = every project
	Limit    int       // default 20
	Semantic bool      // false = FTS-only (the palette's fast path)
	Since    time.Time // zero = all time; otherwise updated at/after this instant
}

// Search finds memories and notes for a human searching the console. It shares
// Recall's candidate queries, RRF fusion, and embedder-degradation contract (see
// candidates) but deliberately drops three Recall behaviors that are wrong for
// this caller:
//
//   - No token budgeting. Recall packs into an agent's context budget and
//     silently truncates; a search page owes the observer every hit it found, up
//     to the limit they asked for.
//   - No post-fusion scope guard. Recall re-checks each hit against its single
//     bound project, which would discard every project-scoped hit of an
//     all-projects search. Scope here is enforced only where it belongs -- in the
//     candidate queries, via Projects.
//   - No link expansion. [[name]] resolution is single-project by construction,
//     and a linked neighbor does not textually match the query, so it reads as
//     noise to someone who typed a search term.
//
// Snippet is populated for hits the FTS leg found; a semantic-only hit has none
// (there is no matched term to quote), and the caller falls back to the item's
// description.
//
// Search adds one guard Recall does not have: the semantic floor
// (search.semantic_floor). The cosine leg is pure nearest-neighbor -- there is
// always a "nearest" item, however far, so without a floor any query fills the
// page to its limit with noise. A semantic-only hit below the floor is dropped;
// a hit the lexical leg also matched earned its place regardless of distance.
// Recall keeps every neighbor on purpose: an agent can judge a weak hit, an
// observer reads "20 results" as 20 matches. Similarity is set on every hit the
// semantic leg found, so the caller can show where relevance falls off.
func (s *Service) Search(ctx context.Context, in SearchInput) ([]Hit, error) {
	kinds := scopeKinds(in.Scope)
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}

	acc, err := s.candidates(ctx, in.Query, kinds, in.Projects, in.Since, searchSourceDepth, in.Semantic, "retrieve.Search")
	if err != nil {
		return nil, err
	}
	ordered := rankFused(acc)

	mems, notes, err := s.hydrate(ctx, ordered, acc)
	if err != nil {
		return nil, err
	}

	out := make([]Hit, 0, limit)
	for _, id := range ordered {
		if len(out) >= limit {
			break
		}
		f := acc[id]
		if f.semantic && !f.fts && f.cosine < s.search.SemanticFloor {
			continue
		}
		var h Hit
		if f.kind == "note" {
			n, ok := notes[id]
			if !ok {
				continue
			}
			h = noteHit(n)
		} else {
			m, ok := mems[id]
			if !ok {
				continue
			}
			// The candidate queries already drop invalidated memories; this is
			// the residual guard for one superseded between the query and here.
			if !m.Active() {
				continue
			}
			h = memoryHit(m)
		}
		h.Source = fusedSource(f)
		h.Score = f.score
		h.Snippet = f.snippet
		if f.semantic {
			h.Similarity = f.cosine
		}
		out = append(out, h)
	}
	return out, nil
}
