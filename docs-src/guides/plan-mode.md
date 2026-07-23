---
title: Capture Claude Code plans
description: How plan mode is captured automatically - which hook does what, a plan's life from draft to approved, and the escape hatches.
---

Claude Code's plan mode produces exactly the artifact that usually evaporates: a
considered design, written down, immediately before the work starts and gone
immediately after it. Seamless captures it without you doing anything.

Codex also has a Plan mode, but this automatic artifact capture is
Claude-Code-specific. Seamless has no verified Claude-style plan-file and
`ExitPlanMode` surface to capture from Codex; Codex SubagentStart/Stop hooks
provide bounded constraints and parent heartbeats, not plan notes. A Codex agent
can still compose durable plans explicitly with a `plan:<slug>` note plus tasks.

The result is a [plan composition](/concepts/tasks-and-plans/) - narrative,
supporting context, and steps - built for you as you plan.

## Which hook captures what

| Hook | Fires when | Captures |
|---|---|---|
| `PostToolUse` (`Write`/`Edit`/`MultiEdit`) | A plan file is saved | Upserts a `cc-plan-<basename>` note - one note per plan, updated on each iteration |
| `PostToolUse` (`ExitPlanMode`) | The plan is approved | Creates the tracking task |
| `PermissionRequest` (`ExitPlanMode`) | You are asked to review the plan | Marks the plan `presented` |
| `SubagentStop` | A planning subagent finishes | Caches its prompt and report as a `cc-agent-<id>` note in the same composition |

The `SubagentStop` capture is the one people do not expect and end up valuing:
when a plan was informed by four research subagents, their findings are usually
lost the moment the planning turn ends. Here they stay attached to the plan, so
the agent that executes it can read the research that justified it.

## A plan's life

<figure class="doc-figure" data-tone="pop" aria-labelledby="plan-lifecycle-caption">
  <span class="figure-kicker">Captured-plan lifecycle</span>
  <div class="doc-flow cols-2">
    <div class="flow-node"><span class="flow-step">1 · plan mode</span><strong>Plan file saved</strong><small>Upserts <code>cc-plan-&lt;name&gt;</code> with <code>plan-status:draft</code>; later saves create iterations.</small></div>
    <div class="flow-node no-arrow"><span class="flow-step">2 · presented</span><strong>Awaiting approval</strong><small>The briefing names the presented plan.</small></div>
  </div>
  <div class="flow-split flow-outcomes">
    <div class="flow-node success no-arrow"><span class="flow-step">3a · approved</span><strong>Tracking begins</strong><small>Status becomes approved and an implementation task is created.</small></div>
    <div class="flow-node warn no-arrow"><span class="flow-step">3b · abandoned</span><strong>Closed deliberately</strong><small>An unapproved plan can be marked abandoned instead of lingering.</small></div>
  </div>
  <figcaption id="plan-lifecycle-caption">A captured plan remains one composition across edits; approval changes its state and opens the execution loop.</figcaption>
</figure>

The statuses are stored as a `plan-status:<value>` tag on the note:
`draft`, `presented`, `approved`, `abandoned`.

## Why unapproved plans show up in the briefing

A captured-but-unapproved plan appears in briefings as:

```text
PLAN (awaiting approval): seamless-documentation-site -- (presented, 2m)
```

This is deliberate. A plan that was designed, presented, and then forgotten is
the most expensive kind of lost work - the thinking already happened. Surfacing
it costs one briefing line and makes the decision explicit: pick it up, or
abandon it on purpose.

Unapproved captures are budget-participating, unlike the pinned lines above them:
a stale hint should lose to your actual memories, not crowd them out.

## The escape hatches

Sometimes Claude Code skips the approval hook, and a plan you did approve stays
`presented`.

```bash
seam plan list                  # every captured plan and its status
seam plan show <slug>           # the narrative + its steps
seam plan check <slug>          # staleness: has the repo moved since capture?
seam plan approve <slug>        # force approval + create the tracking task
```

`seam plan check` compares the git stamp recorded at capture against the repo
now. A plan written against a tree that has since moved on is not automatically
wrong, but it is worth re-reading before executing - that is the question this
answers.

`--cwd` picks the repo to check against and defaults to the current directory. It
goes on either side of the slug: `seam plan check --cwd ~/repos/myproj my-slug`
and `seam plan check my-slug --cwd ~/repos/myproj` are the same line. See the
[seam CLI](/reference/cli-seam/).

You can also browse everything at `/console/plans`.

## Turning it off

Capture is controlled by the `plan_capture` block - see
[Configuration](/reference/configuration/):

- `plan_capture.enabled` - capture at all.
- `plan_capture.auto_task` - create the tracking task on approval.
- `plan_capture.inject_related` - surface related captures in briefings.

All default to on.
