# Write memories that get recalled

> The description is the retrieval surface, not a label - how to write one, how to pick a kind, and the four habits that fill a store with noise.

A memory that is never retrieved is worse than one that was never written: it
cost budget, it cost a write, and it is now something the gardener has to reason
about. Getting recalled is not luck. It is almost entirely a property of the
`description`.

## The description is the retrieval surface

[Memory & notes](https://thereisnospoon.org/docs/concepts/memory/) says the description is the only text an index
ever shows. That is the visible half. The mechanical half is sharper, and it is
where most bad memories die:

| Path | What it reads |
|---|---|
| Briefing, memory list, recall results | The `description`. Nothing else |
| The `<seam-recall>` prompt injection | `name` + `description`, tokenized. **The body is never considered** |
| A `recall` call - keyword leg | `name`, `description`, `body` (FTS5) |
| A `recall` call - vector leg | `name` + `description` + `body`, embedded together |

Read the second row again. The [UserPromptSubmit matcher](https://thereisnospoon.org/docs/reference/hooks/) is
purely lexical - it has to be, it runs inside a five-second hook - and it scores
each memory on the tokens in its name and description alone. A memory whose
description is vague is **invisible to ambient recall** no matter how good its
body is. It can only be found by an agent that already decided to go looking.

The body is not wasted; it is what the agent reads once the description got the
memory into the room. But the description is what does the getting.

Two mechanical details that follow from this:

- The matcher requires at least two distinct shared tokens above an IDF-weighted
  floor, drops stopwords and tokens under three characters, and returns at most
  three hits. Descriptions built from common words compete with everything and win
  nothing. **Rare, specific terms are what score.** Among matches that clear the
  floors, a memory's [utility](https://thereisnospoon.org/docs/concepts/recall/#the-utility-nudge) - its record
  of actually being used - breaks the ties for the three slots.
- A description over 150 characters is **silently truncated**, not rejected. Write
  to the limit deliberately, or the sentence that mattered gets cut mid-word.

## Good and bad, worked

These are real memories from this repository's own store, paired with the vague
version each could have been.

**A gotcha.**

```text
BAD   description: notes about gofmt
GOOD  description: gofmt walks the filesystem while go's ./... skips dot-dirs:
      `gofmt -w .` rewrites other agents' .claude/worktrees mid-edit. Use make fmt.
```

The bad one shares no rare token with any prompt an agent would write. The good
one carries `gofmt`, `dot-dirs`, `worktrees` - an agent typing "why did my
formatter touch files I didn't edit" hits it, and the description *already
contains the fix*. The body never has to open.

**Another gotcha, where the description is the whole answer.**

```text
BAD   description: seamlessd gotcha
GOOD  description: pkill -f 'seamlessd serve' kills the user's launchd daemon
      too, not just your dev instance; match the port or pid instead.
```

**A constraint.**

```text
BAD   description: rules for errcheck
GOOD  description: errcheck runs with check-blank: every `_`-discarded error is
      either in exclude-functions or carries //nolint with a reason. No third category.
```

A constraint is pinned into every briefing and never dropped for budget. That is
expensive real estate, and "rules for errcheck" spends it to tell an agent that
rules exist.

The pattern in every good one: **symptom or trigger, then the mechanism, then the
consequence or the fix.** Not the topic. The topic is what a filename is for.

## Name it the way it will be searched

The name is tokenized alongside the description, so it is retrieval surface too,
not just a filename. `chroma-boot-race` earns its tokens; `note-3` earns nothing.
Kebab-case, unique within the project, and made of the words a future prompt will
actually contain.

## Choosing a kind

The kind is not filing paperwork - it changes what happens to the memory:

| Kind | Use it for | Consequence |
|---|---|---|
| `constraint` | A rule the project cannot violate | **Pinned.** Never dropped for budget, never staleness-archived |
| `stage` | Where multi-session work stands | **Pinned**, same as a constraint |
| `gotcha` | A trap, led by its symptom | Ordinary budget and staleness rules |
| `decision` | A choice plus the alternatives it rejected | Ordinary |
| `refuted` | A belief that turned out false | Ordinary |
| `runbook` / `protocol` / `reference` | A procedure, an agreement, a pointer | Ordinary |

Two calls people get wrong.

**`decision` vs `constraint`.** "We chose SQLite over ChromaDB, because a second
service buys ANN we do not need" is a `decision` - it carries reasoning and a
rejected alternative, and it exists so the argument does not happen again. "No
CGO" is a `constraint` - there is no reasoning to weigh at call time, only a rule
to not violate. Filing a preference as a constraint crowds out real constraints;
filing a real constraint as a `reference` means agents violate it.

**`refuted` is the one that gets skipped.** Nobody wants to write down what they
were wrong about. But a store that only records what is true keeps paying for the
same wrong turn forever: the fleet re-derives the dead end, tries it, finds it
dead, and moves on - every time, for every agent. Recording the refutation makes
that cost one-time. It is the highest-leverage memory kind and the least written.

## Supersede, don't accrete

Four ways to change memory, and the wrong one rots the store:

| You want to | Use |
|---|---|
| Correct or extend what *this* memory says | `memory_write`, same `name` - updated in place, id stable |
| Add to the end without rereading it | `memory_append` |
| Replace a **different**, now-outdated memory | `memory_write` with `supersedes` |
| Remove something written by mistake | `memory_delete` |

**Delete is for things that were never true; supersede is for things that stopped
being true.** Deleting the latter destroys the reasoning that explains the current
state, and the argument reopens in six weeks.

The subtle one is `memory_append`. It grows the body and **does not touch the
description** - which is correct, and also exactly how a memory rots. Append four
times and the description now summarizes the first paragraph of a memory that has
moved on. The retrieval surface has silently decoupled from the content: recall
still finds the memory for the old topic, and never finds it for what it now
mostly says. Append for a genuine addendum; `memory_write` the same name when the
memory's *point* changed, so the description changes with it.

## Four anti-patterns

**Journaling.** "Investigated the retrieval funnel today, found the stats were
session-scoped, fixed it in two commits." Too long to inject, too specific to
generalize, and it displaces a constraint. That is `session_end` findings, or a
note. The test: *would a future agent need this injected before it starts working?*

**Duplicating the codebase.** Seamless stores what agents *learned*, not what the
code says. The code is already in the repo, and a memory mirroring it goes stale
the moment someone edits the file - silently, because nothing links them. The same
goes for anything `CLAUDE.md` already injects: restating it spends budget to say
what the agent was told anyway.

**"Notes about X" descriptions.** Covered above, and worth naming as a habit
rather than a mistake. If the description names a topic instead of making a claim,
it is a label. Labels do not retrieve.

**Accreting near-duplicates.** Writing `console-theme-fix-2` beside
`console-theme-fix` gives recall two answers and lets the agent pick. Seamless
pushes back twice: `memory_write` on a new name reports a semantically similar
existing memory as an advisory hint (the write still proceeds - it is a hint, not
a veto), and [the gardener](https://thereisnospoon.org/docs/concepts/gardener/) proposes a merge for pairs above
its similarity threshold. Take the hint. If the new thing replaces the old thing,
that is what `supersedes` is for.

## The one-line test

Before writing, say the memory out loud as its description alone. If a future
agent - with none of your context, three weeks from now, mid-task - could not
decide from that line whether to read further, the line is not done yet. That is
the description's entire job, and it is the only part of the memory most agents
will ever see.
