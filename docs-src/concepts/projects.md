---
title: Projects & scope
description: How Seamless decides which project a call belongs to - the precedence chain, the fail-closed rule, and project families.
---

A **project** is the scope everything else inherits. Get scope right and agents
never think about it again; get it wrong and knowledge lands where nobody will
find it.

The design goal is that agents almost never pass `project`. In a mapped repo,
scope resolves from where the agent already is.

## The precedence chain

For a durable write, scope resolves in this order - first hit wins:

```text
1. explicit project argument   ─┐
2. the bound session's project  ├─▶  a project slug
3. the ambient session's project┘
4. nothing resolved  ──────────────▶  REJECTED (fail closed)
```

Worked through, rung by rung:

**1. An explicit `project` argument.** You said it, so it happens. This is also
how you deliberately reach another project (`project: otherrepo`) or the global
scope (`project: global`). A slug Seamless has never seen is **not** an error: the
write registers it, and the new project appears in `project_list` and the console
like any other. Naming a project into existence is an ordinary thing to do.

**2. The bound session.** `session_start` binds the connection: every later call
on it inherits that project. This is why an agent that opens a session can then
write memory with no `project` anywhere in sight.

**3. The ambient session.** No explicit binding? Seamless looks for the ambient
session the SessionStart hook opened for this working directory, resolved via the
`repo_project_map` setting. This is the rung that makes mapped repos feel like
magic: an agent that never calls a single session tool still writes to the right
project, because its cwd said which one.

If that lookup is **ambiguous** - more than one candidate session - the call is
rejected rather than guessed.

**4. Nothing.** With no session and no explicit project, a durable write is
**rejected as ambiguous**.

## Why writes fail closed

This is the rule worth understanding, because it is the one that produces
confusing errors.

The tempting alternative is: no scope resolved, so write it globally. That is
wrong. A global memory is seen by **every agent in every repo, forever**. It is a
strong claim, and the last thing you want is for it to be what happens when the
system is *unsure*. Silence should not be consent to the broadest possible scope.

So a write with no resolvable scope errors out and says so. `project: global` is
a token you pass on purpose, never a default you fall into.

The way out of that error is to **name the project** - and if the right one does
not exist yet, name it anyway. This is worth stating plainly because the opposite
guess is so natural: an agent unsure whether an unmapped slug will be rejected
reads `project: global` as the cautious choice, when it is the only choice with
machine-wide blast radius. A new project is cheap, local, and reversible. Global
is forever and everywhere. When in doubt, invent the project.

Reads are more relaxed than writes - a search with no scope has an obvious safe
answer (search what you can see), while a write does not.

## Mapping a repo

```bash
seamlessd map-repo --path ~/code/myrepo --project myrepo
```

This binds a working directory to a project. Afterwards, agents in that repo
inherit its scope through rung 3 without any tool call at all.

## Families: parents and siblings

Projects can have parents, which makes **families**. A family exists so that
related projects can share what is genuinely shared without merging into one
undifferentiated pile.

Two briefing knobs control the cross-over:

- `briefing.include_parent_memories` - a child's briefing carries the parent's
  memories. On by default: a rule that holds for the parent usually holds for the
  child.
- `briefing.sibling_findings_count` / `briefing.include_sibling_memories` - how
  much a sibling's recent work bleeds into yours. Findings cross over by default
  (two per briefing); sibling *memories* do not, because what is true of one
  sibling often is not true of another.

Splitting one project into children is planned as a unit - see
[The gardener](/concepts/gardener/), which handles it as a `split` rather than a
pile of individual moves.

## When scope goes wrong

The symptom is almost always "the agent wrote it somewhere I can't find it" or
"the write was rejected as ambiguous". Both are the chain above:

- **Rejected as ambiguous** - no session bound and no mapped repo. Run
  `session_start`, or map the repo.
- **Landed in the wrong project** - the cwd mapped somewhere unexpected, or an
  explicit `project` overrode what you meant. Rung 1 beats everything.
- **A tool insists a task is claimed by your own session** - the connection
  binding was lost. Re-run `session_start` with the same name to rebind.
