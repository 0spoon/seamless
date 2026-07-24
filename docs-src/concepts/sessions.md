---
title: Sessions & briefings
description: How an agent gets your knowledge injected before it does anything - ambient sessions, the briefing's packing order, and what never gets dropped.
---

A Seamless **session** is one agent's stretch of work. A **briefing** is what
that agent gets handed at the start of it, before it has called a single tool:
a SessionStart hook - installed for [Claude Code](/claude-code/) or
[Codex](/codex-cli/) - resolves the working directory to a project, assembles
constraints, plan rollups, and recent findings inside a token budget, and
injects the result into the agent's context. A client without hooks gets a
briefing only by calling `session_start` itself.

This is the mechanism that makes Seamless ambient rather than opt-in. An agent
does not have to remember to ask what it knows; it begins already knowing your
constraints.

## Ambient vs. explicit sessions

| | Ambient | Explicit |
|---|---|---|
| Opened by | The SessionStart hook, automatically | `session_start` |
| Named | `cc/<prefix>-<digest>` (Claude Code) or `cx/<prefix>-<digest>` (Codex) | Whatever you pass, or generated |
| Gets | The short injected briefing | The full briefing, returned by the call |

The ambient handle keeps the first eight external-ID characters readable and
adds 64 stable SHA-256 bits. Seamless resolves lifecycle activity by the full
external ID plus client, so two UUIDv7 sessions sharing a timestamp prefix never
share scope, findings, or provenance. Pre-upgrade handles keep their old names
when resumed. The handle is a display label, not an API: treat `cc/...` and
`cx/...` as opaque and never parse or construct them.

They are not two competing sessions. `session_start` **adopts** the sole ambient
session for the same working directory rather than opening a second one - that
adoption rule exists because the alternative was double-counting every agent.

Call `session_start` when the work is non-trivial: it returns the full briefing
(the injected one is deliberately shorter), and it binds the connection so
everything afterward inherits the project scope.

That distinction also appears in knowledge provenance. A write attributed by an
ambient hook stores the session's `cc/...` or `cx/...` **name** in
`source_session`; a write made through a bound MCP connection stores the
session's ULID. Readers resolve both forms. Do not infer client, scope, or
liveness from the spelling of `source_session`.

## An actual briefing, annotated

This is a real briefing, in the order the assembler packs it:

<figure class="doc-figure" aria-labelledby="annotated-briefing-caption">
  <div class="sample-panel">
    <div class="sample-panel-head"><span>Actual packing order</span><span>budgeted</span></div>
    <div class="sample-panel-body"><span class="sample-muted">&lt;seam-briefing&gt;</span><br><span class="sample-strong">Seam project: seamless</span> -- 62 memories (24 constraints), 3 recent findings.<br>CONSTRAINT: errcheck-check-blank-two-category-rule: errcheck runs with check-blank ...<br>CONSTRAINT: llm-degradation-remote-vs-local: llm errors split remote ...<br><span class="sample-muted">... 2 more CONSTRAINT lines ...</span><br>Also binding (20): fts-or-vs-allterms-presence-probe, console-csrf-origin-check-contract, ... -- memory_read a name before working near it.<br>STAGE: deep-audit-f15-f18-landed -- status unknown<br>PLAN: marketing -- 2/3 done, 1 claimable, 0 in flight<br>PLAN (awaiting approval): seamless-documentation-site -- (presented, 2m)<br>CONVENTION: wordmark-caret-l-spans-three-files: The wordmark markup must stay in sync ...<br><span class="sample-muted">... 3 more CONVENTION lines ...</span><br>(9 conventions, 4 shown -- recall kind=convention for the rest)<br><br><span class="sample-strong">Recent findings:</span><br>- cc/1fa4b02d (1h): Landed the installer check; the release gate now fails on ...<br><br>Ready tasks: 2 -- Fix tool.call misattribution; Polish the docs nav<br><br><span class="sample-strong">Memories (seamless):</span><br>- gofmt-must-scope-to-tracked-files: gofmt walks the filesystem ...<br>- shared-worktree-concurrent-agents-verify: Agents share the main worktree ...<br>- (+34 older -- use recall)<br>Recall on demand with recall; read a memory with memory_read.<br>Seam session: cc/8dd2fd5b-55d96b8d15ff0104 (ambient)<br><span class="sample-muted">&lt;/seam-briefing&gt;</span></div>
  </div>
  <figcaption id="annotated-briefing-caption">Situation before library: the pinned head leads (tiered constraints, stages, plan rollups), what just happened follows (pending plans, conventions, recent findings, ready tasks), the memory index packs after it, and retrieval guidance plus session identity close the envelope.</figcaption>
</figure>

Line by line:

- **The header** counts what exists, so an agent knows how much it is *not* being
  shown. Constraints are memories too, so they are reported as a subset of the
  total, not a second pool.
- **`CONSTRAINT:` lines** come first and are **never dropped for budget**. A
  constraint is a rule the project cannot violate; a briefing that omitted one to
  fit a token budget would be worse than no briefing at all. They are *tiered*:
  the top `briefing.constraint_max_full` (default 4) render as full
  `name: description` lines - starred constraints first, then constraints a
  recent mishap referenced (last 30 days, most recent first), then the same
  blended recency+utility order the memory index uses - and the remainder
  collapse into the compact **`Also binding (N):`** line, which still names
  every one so an agent can `memory_read` a name before working near it.
  Setting the knob to 0 disables tiering and renders every constraint in full.
- **`STAGE:` lines** are pinned right after constraints, for the same reason: a
  gated stage's status is load-bearing for the whole session. The pin belongs to
  the *gate*: a stage whose body does not open with a live
  `Status: open|in_progress|blocked` header only renders (as `status unknown`)
  for the `briefing.stage_unknown_max_age_days` grace window (default 7) after
  its last update, then leaves the briefing rather than squatting in it forever.
- **`PLAN:` rollups** follow, also pinned. The counts (`2/3 done, 1 claimable`)
  tell the next agent what work it can pick up right now. Starred memories close
  the pinned head as `FAVORITE:` lines, exempt from every trim.
- **`PLAN (awaiting approval)` lines** open the budgeted body: captured but
  unapproved plans are a hint, not a commitment, so unlike the rollups above
  them they compete for budget and expire after
  `briefing.pending_plan_max_days`.
- **`CONVENTION:` lines** follow: project-local choices and layout facts
  (`kind: convention`) - binding, but topically triggered, so unlike
  constraints they compete for budget. The top `briefing.convention_max_full`
  (default 4; 0 renders all) show in full and a count line always closes the
  section, pointing at `recall kind=convention` for the rest.
- **Recent findings** - what previous sessions learned, harvested at their end -
  render right after: they say what just happened here, so they pack (and
  render) before the memory index rather than below it.
- **The ready-tasks line** closes the situation half: the open queue, oldest
  first, before the library begins.
- **The memory index** is `name: description` only - the description is the *only*
  text an index ever shows, which is why writing a good one matters more than
  writing a good body.
- **`(+34 older -- use recall)`** is the honest tail: the index was trimmed, and
  the briefing says so instead of pretending it is complete.

## The budget, and what survives it

Sections are packed against `budgets.max_briefing_tokens` (default 1500), then
the whole thing is hard-capped at `briefing.hard_cap_multiplier` times that
(default 2x).

The **never-drop invariant**: constraints (both the full tier and the compact
`Also binding` line), pinned stages, active-plan rollups, and starred memories
are counted first and are exempt from budget dropping - every constraint name
appears in every briefing. Everything else packs in render order - pending
plans, conventions, recent findings, ready tasks, the memory index, sibling
findings, sibling memories - so budget priority and render priority agree: the sections
that say what is happening now pack before the memory library, a fat index can
no longer evict the findings that render above it, and the sibling sections
are the first to go when the budget runs out. The header counts only the
findings that actually rendered, and findings or index lines cut by budget
leave an explicit `+N more/older -- use recall` trailer.

The memory index's own order starts as newest-first - but each memory also
carries a [utility score](/concepts/recall/#the-utility-nudge), a time-decayed
record of actual demand (reads, recall hits, prompt matches; being briefed
counts for nothing), and once a project's demand history matures the index is
ranked by the blend `(1-w)·recency + w·utility`, both on a 14-day half-life,
with `briefing.utility_weight` as `w` (default 0.4, 0 restores pure recency).
The same blended key ranks constraints ahead of their tier split, after stars
and recent mishaps have claimed the head of the full tier; while utility is
inactive the constraint order degrades to pure recency.
The switch is deliberate, not silent: in `briefing.utility_mode: auto` the
gardener latches it per project only once the project's first demand is at
least 14 days old and it has shown 20+ demand events and 10+ memories touched
in 30 days - a young project keeps the recency order, because a utility signal
with no history behind it is noise. `on`/`off` force it everywhere, and the
console Settings page shows each scope's progress toward the latch, with a
per-scope force.

Every knob is tunable in [Configuration](/reference/configuration/), and the
`briefing:` block is also editable live in the console. Those runtime edits are
stored in the database and win over both the file and the environment, applying
from the next session start without a daemon restart. If a briefing setting seems
to be ignored, check the console before you check the YAML.

Codex adds one client-specific safety ceiling after this packing step: every
model-visible Seamless hook context is capped at 2,400 estimated tokens, below
Codex's approximate 2,500-token temporary-file spill threshold. The cap runs
before telemetry is recorded and preserves generated closing tags and the
ambient handle. Claude Code does not use this additional cap.

## Liveness

Sessions heartbeat. A session that ends cleanly cascades immediately; one that is
abandoned is caught by an idle reaper after `gardener.session_idle_minutes` and
marked `expired`. The TTL only applies when there was no end signal at all - a
crashed agent's session does not sit "live" forever, and a slow one is not
reaped out from under itself.

`session_end` is the explicit findings path. Claude Code's SessionEnd hook calls
the same lifecycle. Codex 0.144.6 has no SessionEnd event, so each `Stop` hook
updates provisional findings from `last_assistant_message` and the reaper later
marks the idle ambient session `expired`; those expired ambient findings still
surface in future briefings. Keep findings tight - briefings show a short preview
- but they are stored in full, not rejected, so a long finding is fine when it
earns it.

The end of a session also harvests **real model token usage** - input, cached,
cache-creation, and output counts read from the client's own transcript
(Claude Code) or rollout file (Codex), not estimated from injected bytes. The
session's console page shows them as its "Model tokens" panel; a session whose
client exposes no transcript simply records none.
