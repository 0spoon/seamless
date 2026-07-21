---
title: Reciprocal rank fusion for agent recall
description: How RRF merges keyword and vector search into one ranked list by rank, not score - and how Seamless runs both legs in a single SQLite file with no vector database.
---

Reciprocal rank fusion (RRF) is a method for merging several ranked result
lists into one: each document scores `1 / (k + rank)` in every list that
returned it, the scores sum, and the constant `k` (60, per the original paper
and most implementations) damps the difference between rank 1 and rank 5.
The point of fusing by *rank* instead of by raw score is that the input lists
never have to agree on what a score means - BM25 values and cosine
similarities live on incomparable scales, and RRF never compares them, only
their orderings.

That property is why RRF is the standard fusion step in hybrid search, and why
it fits agent memory retrieval unusually well. Agents query in two distinct
modes: exact identifiers (`tasks_claim`, `SEAMLESS_DATA_DIR`, an error string)
where keyword search wins and embeddings blur, and paraphrases ("why does the
first query fail on cold start?") where vector similarity wins and keywords
miss. A fused rank means neither mode has to be predicted in advance - a
result that both legs rank highly beats a result only one leg loves.

## How Seamless runs it

Seamless's [recall](/concepts/recall/) - the single search entry point
its agents call - runs both legs inside one SQLite file:

- **Keyword leg:** SQLite FTS5 full-text search over names, descriptions, and
  bodies.
- **Semantic leg:** cosine similarity over float32 embedding BLOBs stored in
  the same database, compared by brute force - no vector database, no ANN
  index, no second process.
- **Fusion:** RRF with `k=60`, over a candidate pool a few multiples of the
  requested limit so the fused order has room to differ from either leg's.

The fused score is the base order, not the final word: a favorite multiplies
its score by 1.15, and a memory's decayed demand adds a
[utility nudge](/concepts/recall/#the-utility-nudge) capped at +10%. Both are
bounded reorderings of what fusion returned - neither can pull in a result the
legs did not.

Brute-force cosine is a deliberate trade: a personal knowledge store holds
thousands of memories, not millions of documents, and at that scale a linear
scan is faster than the operational cost of an approximate index. The same
shape would be the wrong call at corpus scale - this is a design for one
developer's fleet of agents, not a search engine.

The degradation rule matters as much as the fusion: with no embedding provider
configured - or a configured one unreachable - recall runs keyword-only rather
than failing. A memory system that errors when an API key lapses is a memory
system agents learn to stop calling.

For where recall sits among the three delivery paths (briefing, prompt-matched
injection, explicit search), see [recall](/concepts/recall/); for what the
store itself looks like, see [memory & notes](/concepts/memory/).
