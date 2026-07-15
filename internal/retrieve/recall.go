package retrieve

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/llm"
	"github.com/0spoon/seamless/internal/store"
)

// rrfK is the Reciprocal Rank Fusion constant. Fusing by rank (not raw score)
// makes semantic and FTS results comparable despite living on different scales,
// and damps the influence of any single source's top rank.
const rrfK = 60

// recallSourceDepth is how many candidates to pull from each source before
// fusing; a few multiples of the requested limit gives RRF room to reorder.
const recallSourceDepth = 24

// linkExpandFrom is how many of the top fused memory hits are scanned for
// [[name]] links; each linked neighbor is pulled in as a third retrieval signal.
const linkExpandFrom = 5

// Hit is one recall result. Kind tells the agent how to read it: a memory by its
// Name (memory_read), a note by its ID (notes_read).
type Hit struct {
	Kind        string  `json:"kind"` // "memory" | "note"
	ID          string  `json:"id"`
	Name        string  `json:"name"`  // memory name / note slug
	Title       string  `json:"title"` // display title
	Description string  `json:"description"`
	Project     string  `json:"project,omitempty"`
	Age         string  `json:"age"`
	Source      string  `json:"source"` // semantic | fts | fused
	Score       float64 `json:"score"`
	// Snippet is the matched text in context, with the matched terms wrapped in
	// store.SnippetStartMark/SnippetEndMark. Only Search sets it, and only for
	// hits an FTS leg found -- Recall always leaves it empty, so omitempty keeps
	// the MCP recall payload byte-identical to what it was before Search existed.
	// It is RAW item text: a renderer must HTML-escape before substituting the
	// sentinels for markup.
	Snippet string `json:"snippet,omitempty"`
}

// RecallInput parameterizes a recall. Project is the session's bound scope;
// results are limited to that project plus global items.
type RecallInput struct {
	Query   string
	Project string
	Scope   string // all | memories | notes (default all)
	Limit   int
}

type fusedItem struct {
	kind     string
	score    float64
	semantic bool
	fts      bool
	linked   bool // pulled in via a [[name]] link from a top hit
	snippet  string
}

// candidates runs both retrieval legs and fuses them with RRF, returning the
// accumulator keyed by item id. It is the shared heart of Recall and Search:
// the semantic/FTS legs, the RRF weighting, and -- critically -- the embedder
// degradation contract (a local ErrConfig surfaces; a remote failure warns and
// falls back to lexical-only) live here once, so the two entry points cannot
// drift on the behavior that matters most when the provider misbehaves.
//
// semantic requests the cosine leg; it is skipped when no embedder is
// configured. projects/kinds are applied inside the candidate queries (never
// after fusion) so the whole depth stays in scope.
//
// The FTS leg always asks for snippets: the snippet projection cannot perturb
// which rows return or in what order (pinned by
// store.TestFTSSearch_SnippetVariantMatchesPlainOrdering), so Recall paying for
// a snippet it discards is cheaper than two divergent query paths.
func (s *Service) candidates(ctx context.Context, query string, kinds, projects []string, depth int, semantic bool, who string) (map[string]*fusedItem, error) {
	acc := make(map[string]*fusedItem)
	add := func(itemID, kind string, rank int, isSemantic bool, snippet string) {
		f := acc[itemID]
		if f == nil {
			f = &fusedItem{kind: kind}
			acc[itemID] = f
		}
		f.score += 1.0 / float64(rrfK+rank+1)
		if isSemantic {
			f.semantic = true
		} else {
			f.fts = true
			if snippet != "" && f.snippet == "" {
				f.snippet = snippet
			}
		}
	}

	if semantic && s.embedder != nil {
		qvec, err := s.embedder.Embed(ctx, query)
		switch {
		case errors.Is(err, llm.ErrConfig):
			// A client that cannot build its own request is a local defect, not
			// a provider outage. Degrading here would trade a loud failure for
			// quietly worse results on every recall for the life of the daemon,
			// with nothing to tell the owner semantic search had stopped. The
			// llm factories reject a bad base_url at construction, so this is
			// unreachable in a correctly built client -- which is why reaching
			// it must surface rather than hide.
			return nil, fmt.Errorf("%s: %w", who, err)
		case err != nil:
			// The provider is unreachable, throttling, or rejecting the key:
			// nothing here is wrong and it may clear on its own, so lexical-only
			// results are honest and better than none. seamlessd doctor's
			// embedderCheck is where the owner sees this standing condition.
			s.logger.Warn(who+": embed failed, FTS only", "error", err)
		default:
			hits, err := store.CosineSearch(ctx, s.db, qvec, s.embedder.Model(), kinds, projects, depth)
			if err != nil {
				return nil, err
			}
			for rank, h := range hits {
				add(h.ItemID, h.Kind, rank, true, "")
			}
		}
	}
	ftsHits, err := store.FTSSearchSnippets(ctx, s.db, query, kinds, projects, depth)
	if err != nil {
		return nil, err
	}
	for rank, h := range ftsHits {
		add(h.ItemID, h.Kind, rank, false, h.Snippet)
	}
	return acc, nil
}

// hydrate loads the index rows behind a fused accumulator, split by kind.
func (s *Service) hydrate(ctx context.Context, ordered []string, acc map[string]*fusedItem) (map[string]core.Memory, map[string]core.Note, error) {
	var memIDs, noteIDs []string
	for _, id := range ordered {
		if acc[id].kind == "note" {
			noteIDs = append(noteIDs, id)
		} else {
			memIDs = append(memIDs, id)
		}
	}
	mems, err := store.MemoriesByIDs(ctx, s.db, memIDs)
	if err != nil {
		return nil, nil, err
	}
	notes, err := store.NotesByIDs(ctx, s.db, noteIDs)
	if err != nil {
		return nil, nil, err
	}
	return mems, notes, nil
}

// Recall fuses semantic (cosine) and FTS results with RRF, hydrates the winners
// from the index, and packs them into the recall token budget. Scope and
// validity are both enforced inside the candidate queries, so the fused depth is
// entirely in-scope and entirely live. With no embedder configured it degrades
// to FTS only.
func (s *Service) Recall(ctx context.Context, in RecallInput) ([]Hit, error) {
	kinds := scopeKinds(in.Scope)
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}

	// The recall scope is the bound project plus global (project ""). Filtering
	// happens in the candidate queries themselves: filtering only after fusion
	// over a fixed depth would let a query dominated by out-of-scope hits starve
	// in-scope matches that rank deeper than recallSourceDepth.
	projects := []string{""}
	if in.Project != "" {
		projects = append(projects, in.Project)
	}

	acc, err := s.candidates(ctx, in.Query, kinds, projects, recallSourceDepth, true, "retrieve.Recall")
	if err != nil {
		return nil, err
	}
	ordered := rankFused(acc)

	mems, notes, err := s.hydrate(ctx, ordered, acc)
	if err != nil {
		return nil, err
	}

	// Link expansion: pull in memories referenced by [[name]] links in the top
	// hits, as a third retrieval signal alongside semantic and FTS. Requires the
	// body reader (index rows carry no body); degrades away when it is unset.
	// expandLinks adds neighbors to both acc and mems, so we only re-rank.
	if neighbors, err := s.expandLinks(ctx, ordered, acc, mems, in.Project); err != nil {
		return nil, err
	} else if len(neighbors) > 0 {
		ordered = rankFused(acc)
	}

	budget := s.budgets.RecallBudgetTokens
	if budget <= 0 {
		budget = 1000
	}

	out := make([]Hit, 0, limit)
	used := 0
	for _, id := range ordered {
		f := acc[id]
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
			// The candidate queries already drop invalidated memories, and link
			// expansion resolves through MemoryByName, which only returns active
			// ones; this is the residual guard for a memory superseded between
			// the candidate query and this hydration.
			if !m.Active() {
				continue
			}
			h = memoryHit(m)
		}
		// The candidate queries already filter to scope; this is a final guard
		// for link-expanded neighbors and any index/fts project drift.
		if !scopeVisible(h.Project, in.Project) {
			continue
		}
		h.Source = fusedSource(f)
		h.Score = f.score

		cost := estTokens(h.Title + h.Description)
		if len(out) > 0 && (len(out) >= limit || used+cost > budget) {
			break
		}
		out = append(out, h)
		used += cost
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// rankFused returns item ids ordered by fused score, ties broken by id for
// determinism.
func rankFused(acc map[string]*fusedItem) []string {
	ids := make([]string, 0, len(acc))
	for id := range acc {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		si, sj := acc[ids[i]].score, acc[ids[j]].score
		if si != sj {
			return si > sj
		}
		return ids[i] < ids[j]
	})
	return ids
}

func fusedSource(f *fusedItem) string {
	switch {
	case f.semantic && f.fts:
		return "fused"
	case f.semantic:
		return "semantic"
	case f.fts:
		return "fts"
	case f.linked:
		return "link"
	default:
		return "fused"
	}
}

func memoryHit(m core.Memory) Hit {
	return Hit{
		Kind: "memory", ID: m.ID, Name: m.Name, Title: m.Name,
		Description: sanitizeField(m.Description, 200), Project: m.Project,
		Age: humanAge(m.Updated),
	}
}

func noteHit(n core.Note) Hit {
	return Hit{
		Kind: "note", ID: n.ID, Name: n.Slug, Title: n.Title,
		Description: sanitizeField(n.Description, 200), Project: n.Project,
		Age: humanAge(n.Updated),
	}
}

// scopeVisible reports whether an item in hitProject is visible to a session
// bound to scope: global items (project "") are always visible; otherwise the
// projects must match.
func scopeVisible(hitProject, scope string) bool {
	if hitProject == "" {
		return true
	}
	return hitProject == scope
}

func scopeKinds(scope string) []string {
	switch scope {
	case "memories":
		return []string{"memory"}
	case "notes":
		return []string{"note"}
	default:
		return []string{"memory", "note"}
	}
}
