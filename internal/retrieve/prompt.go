package retrieve

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/0spoon/seamless/internal/store"
)

// Prompt-matcher tunables. The matcher is pure lexical (no embedder) so it fits
// the user-prompt-submit hook's tight latency budget.
const (
	promptMinOverlap  = 2                // distinct shared tokens required
	promptMinScore    = 1.5              // IDF-weighted score floor
	promptCorpusTTL   = 30 * time.Second // corpus rebuild interval per project
	promptMinTokenLen = 3
	promptMaxHits     = 3
)

// promptStopwords are ignored during tokenization; they carry little signal and
// would inflate spurious overlap.
var promptStopwords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "are": {}, "but": {}, "not": {}, "you": {},
	"all": {}, "any": {}, "can": {}, "has": {}, "had": {}, "was": {}, "our": {},
	"out": {}, "use": {}, "how": {}, "why": {}, "who": {}, "get": {}, "got": {},
	"this": {}, "that": {}, "with": {}, "from": {}, "into": {}, "your": {},
	"have": {}, "will": {}, "when": {}, "what": {}, "were": {}, "they": {},
	"them": {}, "then": {}, "than": {}, "some": {}, "such": {}, "just": {},
	"like": {}, "over": {}, "also": {}, "only": {}, "does": {}, "did": {},
	"please": {}, "about": {}, "would": {}, "could": {}, "should": {},
}

// corpusCache holds one IDF corpus per project scope, rebuilt after the TTL.
type corpusCache struct {
	mu      sync.Mutex
	entries map[string]*promptCorpus
}

func newCorpusCache() *corpusCache {
	return &corpusCache{entries: make(map[string]*promptCorpus)}
}

type promptCorpus struct {
	builtAt    time.Time
	candidates []promptCand
	idf        map[string]float64
}

type promptCand struct {
	id          string
	name        string
	description string
	updatedAt   time.Time
	tokens      []string
	tokenSet    map[string]struct{}
}

type promptHit struct {
	id          string
	name        string
	description string
	updatedAt   time.Time
	score       float64
}

// PromptRecall matches a user's prompt against the active memory index for the
// cwd's project and returns a <seam-recall> block (or "" when nothing clears the
// overlap and score floors). The second return value is the ids of the memories
// surfaced, so the caller can record them as a retrieval.injected event. It never
// errors on the hook path except on a store failure.
func (s *Service) PromptRecall(ctx context.Context, cwd, prompt string) (string, []string, error) {
	tokens := promptTokenize(prompt)
	if len(tokens) == 0 {
		return "", nil, nil
	}
	project, err := store.ResolveProjectForCWD(ctx, s.db, cwd)
	if err != nil {
		return "", nil, err
	}
	corpus, err := s.promptCorpusFor(ctx, project)
	if err != nil {
		return "", nil, err
	}
	hits := scorePrompt(tokens, corpus)
	return renderPromptRecall(hits), promptHitIDs(hits), nil
}

// promptHitIDs returns the memory ids of the surfaced hits, in render order.
func promptHitIDs(hits []promptHit) []string {
	if len(hits) == 0 {
		return nil
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.id
	}
	return ids
}

// promptCorpusFor returns a cached corpus for the project scope, rebuilding it
// when missing or older than the TTL. The store read happens without the lock
// held so a prompt never serializes behind an in-flight rebuild.
func (s *Service) promptCorpusFor(ctx context.Context, project string) (*promptCorpus, error) {
	s.corpus.mu.Lock()
	if c, ok := s.corpus.entries[project]; ok && time.Since(c.builtAt) < promptCorpusTTL {
		s.corpus.mu.Unlock()
		return c, nil
	}
	s.corpus.mu.Unlock()

	c, err := s.buildPromptCorpus(ctx, project)
	if err != nil {
		return nil, err
	}
	s.corpus.mu.Lock()
	s.corpus.entries[project] = c
	s.corpus.mu.Unlock()
	return c, nil
}

func (s *Service) buildPromptCorpus(ctx context.Context, project string) (*promptCorpus, error) {
	mems, err := store.ActiveMemories(ctx, s.db, project)
	if err != nil {
		return nil, err
	}
	var cands []promptCand
	df := make(map[string]int)
	for _, m := range mems {
		toks := promptTokenize(m.Name + " " + m.Description)
		if len(toks) == 0 {
			continue
		}
		set := make(map[string]struct{}, len(toks))
		for _, t := range toks {
			set[t] = struct{}{}
		}
		for t := range set {
			df[t]++
		}
		cands = append(cands, promptCand{
			id: m.ID, name: m.Name, description: m.Description, updatedAt: m.Updated,
			tokens: toks, tokenSet: set,
		})
	}
	numDocs := len(cands)
	idf := make(map[string]float64, len(df))
	for t, d := range df {
		idf[t] = 1 + math.Log(float64(numDocs)/(1+float64(d)))
	}
	return &promptCorpus{builtAt: time.Now(), candidates: cands, idf: idf}, nil
}

// scorePrompt scores each candidate by the length-normalized IDF sum of its
// overlap with the prompt tokens, keeping only those over both floors, best
// first (ties broken newest).
func scorePrompt(promptTokens []string, c *promptCorpus) []promptHit {
	promptSet := make(map[string]struct{}, len(promptTokens))
	for _, t := range promptTokens {
		promptSet[t] = struct{}{}
	}
	var hits []promptHit
	for _, cand := range c.candidates {
		overlap := 0
		var score float64
		for t := range promptSet {
			if _, ok := cand.tokenSet[t]; ok {
				overlap++
				score += c.idf[t]
			}
		}
		if len(cand.tokens) > 0 {
			score /= math.Sqrt(float64(len(cand.tokens)))
		}
		if overlap < promptMinOverlap || score < promptMinScore {
			continue
		}
		hits = append(hits, promptHit{
			id: cand.id, name: cand.name, description: cand.description,
			updatedAt: cand.updatedAt, score: score,
		})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].updatedAt.After(hits[j].updatedAt)
	})
	if len(hits) > promptMaxHits {
		hits = hits[:promptMaxHits]
	}
	return hits
}

// renderPromptRecall renders matched hits as a <seam-recall> block, or "" when
// there are none (Claude Code ignores an empty additionalContext).
func renderPromptRecall(hits []promptHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<seam-recall>Seam has possibly relevant memories:")
	for _, h := range hits {
		fmt.Fprintf(&b, "\n- %s (%s): %s",
			sanitizeField(h.name, 80), humanAge(h.updatedAt), sanitizeField(h.description, 160))
	}
	b.WriteString("\nRead with memory_read before re-deriving.</seam-recall>")
	return b.String()
}

// promptTokenize lowercases, splits on non-alphanumeric runes, and drops short
// tokens and stopwords. Duplicates are preserved so candidate length reflects
// the real token count for normalization.
func promptTokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len([]rune(f)) < promptMinTokenLen {
			continue
		}
		if _, stop := promptStopwords[f]; stop {
			continue
		}
		out = append(out, f)
	}
	return out
}
