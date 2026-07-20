---
name: seam-research
description: "Run a systematic engineering investigation as a shared Seamless research lab. Use for repeated experiments whose expected and actual results must be compared or outlive the session, including flaky races, firmware bring-up, configuration sweeps, and performance tuning. Do not use for ordinary debugging where one fix ends the investigation."
---

# Run a Seamless research lab

Use Seamless's `lab_open`, `trial_record`, and `trial_query` MCP tools to keep an
immutable, structured lab notebook shared with every agent investigating the
same problem.

Codex invokes this skill as `$seam-research <lab-name> <problem>`; Claude Code
invokes it as `/seam-research <lab-name> <problem>`. Use the arguments from the
user's invocation. Claude Code may also expand them here: `$ARGUMENTS`.

Parse the first word as the lab name and the remainder as the goal. With only a
lab name, resume that investigation. If the skill activated implicitly with no
arguments, derive a short stable name, tell the user, and continue.

## 1. Open or resume the lab

Call `lab_open` with the name and goal. It binds the lab to this MCP connection
and returns recent trials. If trials exist, call `trial_query` for the full
history and summarize:

- trial count and observed outcome groups;
- hypotheses the evidence eliminates;
- durable decision memories mentioning the lab, found with `recall`;
- the last trial and the most useful next unknown.

## 2. Run the evidence loop

Before each trial:

1. Call `trial_query`; filter by outcome or metrics when the history is long.
2. Identify the variable being isolated and what prior trial makes it useful.
3. State the predicted outcome before running the experiment. Include a
   confidence level, quantitative expectation when possible, and the result
   that would change the model.

After observing the result, call `trial_record` exactly once. Trial records are
immutable. Supply:

- `title`: short and specific;
- `changes`: exact files, settings, commands, or hardware changes;
- `expected`: the prediction stated before the run, unchanged;
- `actual`: observed behavior, measurements, and relevant log lines;
- `outcome`: `pass`, `fail`, `partial`, or `inconclusive`;
- `metrics`: queryable numeric or categorical values as a structured object.

Record an abandoned trial as `inconclusive` with the blocker so another agent
does not repeat it blindly. Compare expected and actual immediately; a mismatch
is evidence, not a footnote.

## 3. Distill decisions without duplicating the lab

When evidence settles a direction, use `memory_write` with `kind=decision`.
Name the lab and decision, and record the rationale, rejected alternatives, and
supporting trial titles. Use `gotcha` for a discovered failure mechanism and
`refuted` for a belief the trials disproved. Do not copy the trial log into a
memory; it remains queryable in the lab.

Look explicitly for variable isolation, contradictions, diminishing returns,
and missing coverage. Lead user updates with the current evidence and flag
surprises immediately.

## 4. Coordinate concurrent investigators

Always query before experimenting; another agent may already have run the
cheapest useful trial. When hypotheses are enumerable, create one task per
hypothesis and call `tasks_claim` before testing so concurrent agents cannot
duplicate the same run. If Seamless reports an ambiguous actor, pass this
session's `cc/...` or `cx/...` handle from the briefing.

## 5. Close a useful stopping point

Summarize the evidence and remaining uncertainty. Write only the distilled
durable memories justified above, then use `session_end` with concise findings
when an explicit handoff is appropriate. The lab stays open: resume later with
`$seam-research <lab-name>` in Codex or `/seam-research <lab-name>` in Claude
Code.
