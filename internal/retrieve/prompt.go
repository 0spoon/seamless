package retrieve

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/0spoon/seamless/internal/store"
)

// Prompt-matcher tunables. The matcher is pure lexical (no embedder) so it fits
// the user-prompt-submit hook's tight latency budget.
//
// promptCorpusTTL is a rebuild interval, NOT a staleness bound. Once a project
// has a corpus, a lapsed TTL is served from the stale copy while the rebuild
// runs behind the hook (see promptCorpusFor), so the worst case a prompt can see
// is TTL + rebuild time -- or TTL + promptCorpusRetryBackoff + rebuild time when
// a background rebuild fails. That unbounded-by-TTL staleness is the price of a
// hook that never pays for a cold rebuild; a memory written now may miss the
// next prompt's recall by a few seconds more than the TTL suggests.
const (
	promptMinOverlap  = 2                // distinct shared tokens required
	promptMinScore    = 1.5              // IDF-weighted score floor
	promptCorpusTTL   = 30 * time.Second // corpus rebuild interval per project
	promptMinTokenLen = 3
	promptMaxHits     = 3

	// promptCorpusRefreshTimeout bounds a background rebuild. It is generous
	// (the pass is one indexed read plus tokenization) and exists only so a
	// wedged store cannot hold the single-flight claim forever.
	promptCorpusRefreshTimeout = 10 * time.Second
	// promptCorpusRetryBackoff keeps a failing rebuild off the hot path: after a
	// failure the project is not retried until it elapses, instead of re-arming on
	// the very next prompt and hammering a store that is already unhappy. Flat, not
	// exponential -- the failure mode this guards (store down) is not worth the
	// per-project attempt bookkeeping an exponential curve would need.
	promptCorpusRetryBackoff = time.Minute
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
// Every field is guarded by mu except builds, which is atomic so a test can read
// it without reaching for the cache's lock.
type corpusCache struct {
	mu      sync.Mutex
	entries map[string]*promptCorpus
	// refreshing holds the projects with a background rebuild already in flight.
	// It is the single-flight claim: a burst of expired prompts spawns one
	// rebuild goroutine, not one per prompt.
	refreshing map[string]struct{}
	// retryAfter holds, per project, the earliest time a failed background rebuild
	// may be attempted again. Without it a store that stays down would re-arm a
	// doomed rebuild on every single prompt.
	retryAfter map[string]time.Time
	// builds counts corpus build attempts, cold and background alike. Nothing in
	// production reads it; it exists because the single-flight invariant is
	// otherwise unobservable from outside buildPromptCorpus.
	builds atomic.Int64
}

func newCorpusCache() *corpusCache {
	return &corpusCache{
		entries:    make(map[string]*promptCorpus),
		refreshing: make(map[string]struct{}),
		retryAfter: make(map[string]time.Time),
	}
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

// promptCorpusFor returns the project's corpus, keeping the user-prompt-submit
// hook off the rebuild path wherever it can:
//
//   - cold (nothing cached): build synchronously. There is nothing to serve, and
//     an empty corpus would be a fake result dressed up as a real one. The first
//     prompt for a project pays for the build; that is the correct bill.
//   - warm and fresh (within the TTL): serve the cached corpus untouched.
//   - warm and expired: serve the STALE corpus immediately and rebuild behind the
//     hook. A lapsed TTL never lands on a user's prompt as a latency spike; see
//     the promptCorpusTTL comment for what that costs in freshness.
//
// The store read always runs with the lock released, so a prompt never
// serializes behind an in-flight rebuild.
func (s *Service) promptCorpusFor(ctx context.Context, project string) (*promptCorpus, error) {
	s.corpus.mu.Lock()
	if c, ok := s.corpus.entries[project]; ok {
		refresh := false
		if time.Since(c.builtAt) >= promptCorpusTTL {
			// Claim the rebuild under the same lock that publishes it, so two
			// expired prompts cannot both conclude that nobody is rebuilding.
			_, inFlight := s.corpus.refreshing[project]
			refresh = !inFlight && !time.Now().Before(s.corpus.retryAfter[project])
			if refresh {
				s.corpus.refreshing[project] = struct{}{}
			}
		}
		s.corpus.mu.Unlock()
		if refresh {
			s.refreshPromptCorpus(ctx, project)
		}
		return c, nil
	}
	s.corpus.mu.Unlock()

	// Cold. Concurrent cold prompts for the same project may each build -- that is
	// bounded by in-flight hook requests, needs no goroutine, and is not worth
	// serializing prompts behind a lock to avoid.
	c, err := s.buildPromptCorpus(ctx, project)
	if err != nil {
		return nil, err
	}
	s.corpus.mu.Lock()
	s.corpus.entries[project] = c
	s.corpus.mu.Unlock()
	return c, nil
}

// refreshPromptCorpus rebuilds the project's corpus behind the hook. The caller
// must already hold the project's single-flight claim (corpus.refreshing), which
// this releases on every path the build can return through: a stuck claim would
// freeze the corpus at its stale value for the life of the process, which is a
// worse bug than the latency spike this whole path exists to avoid.
//
// ctx is the hook's request context. It is cancelled the moment the hook
// responds (and, since the daemon hands the signal context to the HTTP server as
// its BaseContext, again at shutdown), so capturing it would cancel the rebuild
// on essentially every prompt -- a refresh that reports success and does nothing.
// WithoutCancel keeps its values and drops the cancellation; the timeout below
// is what bounds the work instead.
//
// The goroutine is deliberately not drained at shutdown. The Service has no
// lifecycle to hang a drain on, and there is nothing worth draining: the
// goroutine writes only to the in-memory cache the exiting process is about to
// drop, and *sql.DB is safe against a concurrent Close (a started query finishes
// and Close waits for it; a later one fails and lands in the Warn below).
func (s *Service) refreshPromptCorpus(ctx context.Context, project string) {
	ctx = context.WithoutCancel(ctx)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, promptCorpusRefreshTimeout)
		defer cancel()
		c, err := s.buildPromptCorpus(ctx, project)

		s.corpus.mu.Lock()
		// Releasing the claim and publishing the result in one critical section is
		// what makes single-flight hold: no prompt can ever observe "nobody is
		// rebuilding" beside the stale corpus this build has already replaced.
		delete(s.corpus.refreshing, project)
		if err == nil {
			s.corpus.entries[project] = c
			delete(s.corpus.retryAfter, project)
		} else {
			s.corpus.retryAfter[project] = time.Now().Add(promptCorpusRetryBackoff)
		}
		s.corpus.mu.Unlock()

		if err != nil {
			// Non-fatal, so log rather than return: the prompt was already served a
			// usable stale corpus, and there is no caller left to hand an error to.
			s.logger.Warn("retrieve: prompt corpus refresh failed, serving the stale corpus",
				"project", project, "error", err)
		}
	}()
}

func (s *Service) buildPromptCorpus(ctx context.Context, project string) (*promptCorpus, error) {
	s.corpus.builds.Add(1)
	mems, err := store.ActiveMemories(ctx, s.db, project)
	if err != nil {
		return nil, err
	}
	cands := make([]promptCand, 0, len(mems))
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
		if utf8.RuneCountInString(f) < promptMinTokenLen {
			continue
		}
		if _, stop := promptStopwords[f]; stop {
			continue
		}
		out = append(out, f)
	}
	return out
}
