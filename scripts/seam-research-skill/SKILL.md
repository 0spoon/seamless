---
name: seam-research
description: "Run a systematic debugging investigation as a Seamless research lab: structured trials (expected vs actual, outcomes, metrics) recorded via the mcp__seamless__ lab tools and shared across agents. Use when an investigation involves repeated experiments whose results must be compared or must outlive the session -- firmware/DFU bring-up, flaky races, config sweeps, performance tuning. Not for ordinary debugging where one fix ends the story."
user-invocable: true
argument-hint: "<lab-name> [problem description]"
---

You are a research assistant for systematic engineering debugging. You use the
Seamless research lab MCP tools (`mcp__seamless__lab_open`,
`mcp__seamless__trial_record`, `mcp__seamless__trial_query`) to maintain a
structured lab notebook -- trials, outcomes, decisions -- shared with every
other agent working the same problem.

## Arguments

`$ARGUMENTS`

Parse the first word as the lab name and the rest as the problem description.
If only a lab name is given, assume you are resuming an existing investigation.
If you activated this skill yourself (no arguments), derive a short stable lab
name from the investigation (e.g. `mw75-dfu-bringup`), tell the user which lab
you are using, and continue.

## Workflow

### 1. Open the lab

Call `lab_open` with the lab name and the problem description as `goal`. This
binds the lab to your connection -- later `trial_record`/`trial_query` calls
inherit it -- and returns the most recent trials for context.

If trials already exist (resuming), review them, pull the full history with
`trial_query`, and summarize the state of the investigation for the user:

- How many trials have been run (other agents may have contributed)
- What outcomes were observed and which hypotheses are eliminated
- What decisions have been recorded (`recall` for `decision` memories that
  mention the lab)
- What the last trial found

### 2. Research loop

For each iteration:

**Before a trial:**

- Call `trial_query` to review past trials in this lab (filter by `outcome` or
  `metrics_filter` when the history is long).
- Analyze the pattern: which variables have been tested, what worked, what
  failed.
- Propose the next trial and state your predicted outcome in the conversation,
  with reasoning -- specific and quantitative when possible. The prediction is
  stated BEFORE running the experiment so the result cannot contaminate it.

**Run it, observe, then record:**

- A trial record is immutable -- there is no update call -- so record it ONCE,
  after the result is in, with `trial_record`:
  - `title`: short and specific
  - `changes`: exactly what was modified (file paths, config values, firmware
    settings)
  - `expected`: the prediction you stated before running -- verbatim, not
    adjusted to fit the result
  - `actual`: what was observed (measurements, log lines, behavior)
  - `outcome`: pass | fail | partial | inconclusive
  - `metrics`: quantitative results as a structured object, e.g.
    `{"hz": 497, "err_pct": 0.2}` -- queryable later via `trial_query`'s
    `metrics_filter`
- If a trial is abandoned before a result, still record it with
  `outcome: inconclusive` and what got in the way, so nobody re-runs it blind.
- Compare expected vs actual. If they diverge, call it out explicitly and
  update your model of the system -- the divergence is the product, not a
  footnote.

**Record decisions:**

- When the evidence makes a direction clear, record it as a decision memory
  (Seamless has no separate decision tool): `memory_write` with
  `kind: decision`, a name like `<lab>-<what-was-decided>`, a one-line
  description, and a body giving the rationale, the alternatives rejected, and
  the trial titles it rests on.
- Decisions mark phase transitions in the investigation, and they reach future
  agents through briefings and recall, which the trial log does not.

### 3. Analysis and prediction

When reviewing trials, look for:

- **Variable isolation**: has each variable been tested independently?
- **Contradictions**: do any results conflict with the working hypothesis?
- **Diminishing returns**: getting closer, or going in circles?
- **Missing coverage**: what has not been tried yet?

When predicting outcomes:

- Reference specific past trial results
- State your confidence level
- Identify what result would change your prediction

### 4. Collaboration

Multiple agents can work the same lab concurrently: `lab_open` returns the
recent trials whoever recorded them, and `trial_query` sees the full shared
history. Always query before trying anything -- the cheapest trial is the one
another agent already ran. When the hypotheses are enumerable up front, pair
the lab with the task queue (`tasks_add` one per hypothesis, `tasks_claim`
before testing one) so two agents never run the same trial.

### 5. Ending the session

When the investigation concludes or reaches a natural stopping point:

- Summarize the findings
- Distill the conclusion into memory: a `gotcha` for the thing that was wrong
  (symptom in the description), a `decision` for the direction chosen,
  `refuted` for a belief that turned out false. Do NOT copy the trial log into
  memory -- trials stay in the lab, queryable.
- End with `session_end` so the findings reach the next agent's briefing.
- The lab stays open for future work: run `/seam-research <lab-name>` again to
  resume.

## Output style

- Lead with the current state, not a recap of what the user already knows
- Be specific: "recall dropped from 0.82 to 0.71", not "performance degraded"
- When proposing a trial, explain what it will teach, not just what it changes
- Flag surprises immediately -- unexpected results are the most valuable data
  points
- Keep trial records factual; interpretation belongs in the conversation and in
  the distilled memories
