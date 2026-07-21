---
title: Memory supersession
description: How an agent memory store stays true instead of just growing - new knowledge explicitly replaces old, with provenance, unlike decay scores or append-only logs.
---

Memory supersession is the lifecycle rule that keeps an AI agent's long-term
memory store *true* rather than merely large: when new knowledge contradicts or
refines old knowledge, the new memory explicitly replaces the old one. The
superseded memory is marked invalid, points at its replacement, and drops out
of every index and retrieval path - but stays on disk as provenance. The store
converges on current truth while keeping the audit trail of how it got there.

In Seamless, supersession is one write:

```
memory_write name=new-truth supersedes=old-truth
```

The old memory gets `invalid_at` set and `superseded_by` pointing at the new
one's ID, plus a tombstone line naming the replacement; from that moment it is
out of session briefings and [recall](/concepts/recall/), but the markdown
file remains readable - and, because the store is
[files in a folder you own](/concepts/memory/), the whole exchange is one
`git diff`.

## Versus forgetting curves and decay scores

Some memory systems forget the way Ebbinghaus curves describe humans
forgetting: relevance decays with time and disuse, and old, rarely-touched
memories fade out of retrieval. The premise is wrong for engineering knowledge.
Being old is not being wrong - a constraint recorded a year ago and referenced
never ("do not set these cookies to SameSite=Strict") can be permanently
correct, and quietly decaying it away re-arms the exact mistake it was written
to prevent. Being recent is not being right, either. Decay forgets by
attrition; supersession forgets by *contradiction*: a memory leaves the store
when something replaces it, not when it has a birthday.

## Versus append-only logs

The opposite failure is never forgetting: append-only stores where both the old
answer and the new one match every future search, and each retrieval - every
session, forever - has to re-adjudicate which is current. Supersession
adjudicates once, at write time, when the contradiction is actually in front of
the writer, and every later reader inherits the verdict.

## The honest limit

Supersession requires the writer to *notice* the contradiction - an agent that
writes a new memory without realizing an old one disagrees leaves both live.
Seamless backstops this two ways: the arbiter ranks conflicting memories when
they collide at retrieval, and the [gardener](/concepts/gardener/) runs
dedup and staleness passes that *propose* supersessions - it never applies
them. A related kind, `refuted`, preserves beliefs that turned out wrong as
standing warnings rather than deleting them. The full lifecycle - archive
versus supersede versus delete - is in
[memory & notes](/concepts/memory/).
