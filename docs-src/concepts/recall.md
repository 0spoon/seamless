---
title: Recall
description: One search entry point fusing keyword and vector search - and the three different ways your knowledge actually reaches an agent.
---

`recall` is the only search tool. There is no separate keyword search, no
separate semantic search, no "advanced" variant. One entry point, because a
surface with three search tools is a surface where agents pick the wrong one.

## How it works

Two retrievers run over the store and their rankings are fused:

- **FTS5 keyword search.** Exact terms, names, error strings - the things vector
  search is worst at. If you know a memory is called `chroma-boot-race`, this is
  what finds it.
- **Vector similarity.** Embeddings stored as float32 blobs in SQLite, compared
  by brute-force cosine. This finds the memory that is *about* your problem when
  you don't know its name.

Their results are combined with **reciprocal rank fusion**: each retriever's
ranking contributes, and something both rank highly wins. Neither retriever gets
a veto, which matters because each is confidently wrong in a different way.

The result set is then packed into `budgets.recall_budget_tokens`.

### About that brute-force scan

Cosine similarity is computed by scanning every candidate vector. No ANN index,
no approximate nearest neighbours, no vector database. For a personal knowledge
store this is simply fast enough, and it buys exactness and one fewer moving
part. It is a deliberate trade, not an oversight - and it is the kind of thing
worth saying out loud rather than hiding behind the word "hybrid".

## The recall triad

This is the part worth internalizing: **searching is only one of three ways your
knowledge reaches an agent**, and they are easy to confuse because all three end
with the agent knowing something.

| | What fires it | What it delivers | Agent asked? |
|---|---|---|---|
| **The briefing** | SessionStart hook | Constraints, pinned stages, plan rollups, the memory index | No |
| **Recall injection** | UserPromptSubmit hook, when a prompt matches stored memories | A `<seam-recall>` block with the matching memories | No |
| **A recall call** | The agent calls `recall` | Ranked, fused, budgeted results | Yes |

The first two are **ambient**: they happen whether or not the agent thinks to
ask. That is the whole point of the hooks. An agent that never calls a Seamless
tool still starts with your constraints and still gets relevant memories surfaced
when its prompt touches them.

The third is what an agent does when it wants something specific. Before you go
looking for something in your own knowledge base by hand, ask an agent to
`recall` it - that is faster and more accurate than making you search.

Read what was already injected before searching again. The briefing is in
context; re-recalling it just spends tokens to learn what the agent was told.

## Degradation is deliberate

Recall keeps a distinction that is easy to get wrong: **a remote failure degrades,
a local misconfiguration surfaces.**

- The embedding provider is unreachable, rate-limited, or rejects the key →
  **degrade**. Recall falls back to keyword-only results. You get worse ranking,
  not an error, because a partial answer beats no answer when the network is
  having a bad day.
- The configuration itself is wrong → **surface it**. This is not a transient
  condition that retrying fixes, and silently degrading forever would hide a
  problem only you can fix.

The failure mode this avoids: a store that has quietly been keyword-only for
three weeks because nobody noticed the embedding key expired.

## Writing memories that get recalled

Recall can only find what was written to be findable. Since the `description` is
the only text an index shows, and it is heavily weighted in matching, it is the
retrieval surface - see [Memory & notes](/concepts/memory/).
