# seambench: the agent-scenario benchmark

`seambench` answers one question with a number: **how much does Seamless
actually help an agent, and did this version help less than the last one?**

It stands up the same seeded fixture several times over -- once per *condition*
-- runs a real agent headlessly in each, grades what the agent did, and reports
the difference between the arms.

> **This is not `make bench`.** `make bench` is the Go hot-path micro-benchmark
> suite: `ns/op`, offline, free, part of the ordinary iterate loop. This is the
> AGENT-SCENARIO benchmark: real `claude` sessions, real API tokens, minutes per
> run. The two share a syllable and nothing else. Say "agent-scenario benchmark"
> or "seambench" in prose; never "the benchmark".

| Half | Does | Costs | Entry point |
| --- | --- | --- | --- |
| `seambench run` | builds the arms, runs scenario x condition x N, captures each run to disk | tokens, minutes | `make seambench` |
| `seambench report` | grades the captured runs, prints uplift + version deltas, records trials | a file read | `go run ./cmd/seambench report` |

The two halves meet **only on disk**. `run` grades nothing; `report` runs
nothing. That is why a grader fix can be re-applied to runs that already cost
their tokens (`report --regrade`), and why a run tree can be graded on another
machine.

Nothing here touches the live `~/.seamless`, `~/.claude`, or `~/.codex`. Every
arm gets its own throwaway `HOME`, demo repo, data dir, key, and non-live port.
The single live write in the whole system is the results trial the *report*
records (see [Trials](#the-one-live-write-trials)).

---

## What is actually being measured

**Conditions (arms).** Three named profiles, built by
`scripts/fixture/harness.sh --mode bench`:

| Arm | What is in it |
| --- | --- |
| `vanilla` | a bare Claude config dir. No Seamless anywhere. The model-only **control**. |
| `mechanism` | everything `install-hooks` wires by default today: hooks (SessionStart briefing + SubagentStart), the MCP registration including its initialize-time server instructions, and the default-installed `seam-onboard`/`seam-research` skill files. |
| `full` | `mechanism` + the `/seam-onboard` CLAUDE.md awareness block, written into that arm's demo repo only. |

So `full` minus `mechanism` is *only* the CLAUDE.md block. The block used to be
the whole difference; server instructions and default-installed skills have
since moved into `mechanism`, and the arms were redefined to match (see the
memory `seambench-architecture`). The `vanilla` and `mechanism` demo repos stay
Seamless-free -- the control must not be able to read its way out of character
(memory `scene-demo-repo-must-be-seamless-free`).

Arms also carry a `client` field (`name[:profile[:client]]`). `claude` is the
only runnable one: `codex exec` cannot run unattended, because hook trust and
MCP tool approval are both interactive-only gates (memory
`codex-headless-two-gates-hooktrust-and-mcp-approval`). A `codex` arm parses and
is then refused, on purpose -- the model is ready for the day the gates are.

**The numbers.**

```
pass-rate(condition)  = passed / graded    within one scenario x condition x version cell
uplift(condition)     = pass-rate(condition) - pass-rate(control)
version delta         = uplift(candidate)  - uplift(baseline)
```

A negative version delta is a **regression**: this version of Seamless helps the
agent less than the baseline did. The `vanilla` arm is in every run for exactly
one reason -- it contains no Seamless, so any movement in *its* pass-rate between
two versions is the model or the environment drifting, and it calibrates how much
of the delta is real.

---

## The scenarios

Five scenarios, one per **mechanism of value**, all on the same `myapp` demo
repo. Each is built to the same discipline: the prompt is something a real
developer would type, the graded surface is small (an attribute, a function,
one file -- never "build a feature"), the correct answer is not derivable from
the repo alone, and the vanilla arm fails for an honest, attributable reason --
under-informed, never sabotaged. A vanilla run that does the right thing anyway
passes; the uplift is a reliability delta, not an impossibility proof.

| Scenario | Mechanism under test | The trap, in one line |
| --- | --- | --- |
| `cookie-hardening` | a constraint memory vetoes a confidently-wrong ticket | the security ticket asks for SameSite=Strict; memory knows Strict logged everyone out once -- keep Lax, harden with `__Host-` instead |
| `stale-assets` | a gotcha steers the fix around invisible infrastructure | `?v=` cache busting is the canonical fix and a silent no-op here: the CDN's cache key ignores query strings -- version the asset *path* |
| `deploy-drain` | continue-work: the plan names the step, the memory its shape | textbook `signal` + `Shutdown` is not graceful *here*: the LB polls `/healthz` on a cadence, so healthz must fail first, then drain, then shut down |
| `restart-logouts` | the two-session handoff (the write half of the loop) | session A diagnoses an incident from an ephemeral log; session B -- fresh tree, log gone -- lands the fix. What A recorded is all B can inherit |
| `refresh-grace` | recorded failed trials rule the tempting fix out | the visible fix is client-side timer hygiene, and it is the recorded dead end; the fix is a server-side grace window in `rotate` |

`restart-logouts` is the suite's only **multi-step** scenario: two headless
sessions against one arm, with runner-materialized *evidence* (an untracked
`logs/app.log`) for session A only and a fresh working tree for session B. The
data dir persists across the boundary -- that persistence is the thing being
measured, and on a vanilla arm nothing persists, which is exactly the control.
Evidence is removed before any diff or capture, so it never appears in a graded
tree; `--timeout` bounds each session, so a two-step run may take twice it.

---

## 0. What you need

- A `claude` binary on `PATH` with working credentials. Every run is a real
  session; there is no offline mode.
- Enough budget. Cost is roughly `scenarios x conditions x N x (one agent
  session)`. The default matrix is every scenario x three arms x 1 run.
- Nothing else. No node, no external services. The optional judge layer wants
  an `internal/llm` provider and is off unless `report --judge` asks for it
  (see [The judge](#the-judge-layer)).

`seambench run -h` lists the scenarios that exist; the list is derived from the
table in `internal/bench`, so it is never stale.

---

## 1. Run the suite

```bash
make seambench
```

That is `seambench run` followed by `seambench report --out` the same tree. It
refuses to start without a `claude` binary and prints a cost warning first.
Knobs:

```bash
make seambench N=5                          # 5 runs per scenario x condition cell
make seambench BASELINE=v0.4.5              # + the version-delta table vs that ref
make seambench CONDITIONS=vanilla,mechanism SCENARIO=cookie-hardening
make seambench REPORT_ARGS=--no-trials      # keep the results on disk only
make seambench RUN_ARGS='--timeout 25m' OUT=/tmp/mybench
```

`OUT` goes to both halves. If you point `RUN_ARGS` at a different `--base`, set
`OUT` too or `report` will look in the default tree.

**`make seambench` is manual-only and must stay that way.** It is not in
`make check`, `make check-fast`, `.githooks/pre-commit`, CI, the release flow, or
any schedule. Automating a token-metered, nondeterministic suite is a separate,
explicit decision that the owner has not taken yet: several monitored manual
runs come first.

Underneath, for the flags the make target does not model:

```bash
go run ./cmd/seambench run [flags]
```

| Flag | Default | Notes |
| --- | --- | --- |
| `--scenario` | all | comma list, checked against the scenario table |
| `--conditions` | `vanilla,mechanism,full` | `name[:profile[:client]]`, same grammar as the harness |
| `-n` | 1 | runs per scenario x condition cell |
| `--base` | `$TMPDIR/seamless-bench` | throwaway base dir the arms are built under |
| `--out` | `<base>/runs` | artifact root for the captured runs |
| `--model` | harness default (`claude-opus-5`) | pinned in every arm's `settings.json`, so runs compare Seamless conditions and never model drift |
| `--port` | 8099 | first port; each Seamless-ful arm takes the next. Live is 8081. |
| `--timeout` | 15m | per-SESSION wall-clock budget for the agent; a multi-step run may take one per step |
| `--version` | `git describe --tags --always --dirty` | the label stamped into every `run.json` |
| `--repo` | git top-level of the cwd | the Seamless checkout holding `scripts/fixture/harness.sh` and `bin/` |
| `--reuse-arms` | off | reuse the arms already under `--base` instead of rebuilding |
| `--no-build` | off | passed through to the harness (reuse the existing `bin/`) |
| `--agent-cmd` | `claude` | the agent CLI. **The dry-run seam** -- see below. |
| `--permission-mode` | `bypassPermissions` | empty omits the flag |
| `--parallel-conditions` | off | run a scenario's arms concurrently -- see [Why the matrix is walked serially](#why-the-matrix-is-walked-serially) |
| `--agent-arg` | -- | repeatable; appended verbatim to the agent command |
| `--baseline REF` | -- | also run the whole suite against that ref (see [Version comparison](#3-version-comparison)) |

**What one run does.** The arms are built once per invocation (the harness copies
a demo repo, seeds an instance, and installs hooks per arm -- expensive and
identical across runs). Each *run* then re-establishes the starting state
itself:

1. `git`-restore the arm's demo repo to the commit captured when the arm was built.
2. On a Seamless-ful arm: wipe the data dir and re-seed it from the scenario's
   own `seedFn`. Leases, finding ages, and plan state are anchored to seeding
   time, so run N+1 sees exactly what run N saw.
3. Start that arm's daemon, run the scenario's agent session(s) headless in the
   demo repo, stop the daemon (which is what lets the event dump read a cleanly
   closed database). A multi-step scenario runs its sessions in order under the
   one daemon: per step, the runner materializes that step's evidence files,
   runs the agent, removes the evidence again (unconditionally, before any diff
   can see it), and -- for a `FreshRepo` step -- resets the working tree to the
   arm snapshot first, so nothing an earlier session left in the TREE carries
   over. The data dir is deliberately never reset between steps.
4. Capture everything into the run directory.

### Why the matrix is walked serially

By default `run` walks scenario x condition x N one cell at a time. Two of those
three dimensions cannot widen, and the third is a deliberate trade:

- **The N runs of a cell SHARE AN ARM.** Every run git-restores that arm's demo
  repo and wipes and re-seeds its data dir to re-establish the starting state, so
  two overlapping runs would reset each other's tree and database mid-flight.
  This is what makes run N+1 see what run N saw; it is not tunable.
- **Scenarios stay serial on purpose.** A systemic failure -- a misconfigured
  arm, an expired credential, an empty briefing -- hits every cell identically,
  so surfacing it after ONE scenario instead of after the whole matrix is worth
  the wall clock. That early exit has already paid for itself once.
- **Condition arms are independent**, with their own demo repo, data dir, HOME,
  config dir and port (vanilla runs no daemon at all). `--parallel-conditions`
  widens that one dimension, for a ~3x speedup on the default three arms.

The cost lands in the metrics: three agent sessions and their daemons contend for
one machine, so `durationMs` inflates while turns, tokens and cost do not. Runs
made that way are stamped `"concurrent": true` in `run.json`, because a
concurrent run's wall clock is not comparable with a serial one's -- including
across the halves of a `--baseline` comparison, where a mode difference would
read as a timing regression. Keep the flag the same on both halves.

At `n=1` on a fast model the whole matrix is minutes and serial is fine. It is
`-n 5` on Opus, where cells stabilize, that the flag earns its contention.

**Dry-running the whole loop without tokens.** `--agent-cmd` points at any
executable that accepts `-p <prompt> --output-format json` and prints a result
envelope. That is how the runner is tested, and it is the right way to check a
harness or Makefile change before spending anything:

```bash
go run ./cmd/seambench run --agent-cmd /path/to/fake-agent --conditions vanilla,mechanism -n 1
```

A fake agent does not fire SessionStart, so a Seamless-ful arm's briefing gate
fails and the uplift comes out negative. That is the dry run working correctly,
not a result.

## 2. Report

```bash
go run ./cmd/seambench report --out /tmp/seamless-bench/runs
```

Grading happens **here**, not in `run`, and reads only the preserved run
directories.

| Flag | Default | Notes |
| --- | --- | --- |
| `--out` | `$TMPDIR/seamless-bench/runs` | the run tree to grade |
| `--json` | `<out>/results.json` | where the results set is exported |
| `--regrade` | off | re-grade every run instead of reusing cached `grade.json` verdicts |
| `--baseline` / `--candidate` | from `versions.json` | version labels for the delta table |
| `--judge` | off | enable the advisory LLM judge on runs graded in THIS pass; cached verdicts skip it, so pair with `--regrade` to judge existing runs |
| `--judge-config` | the standard config search order | Seamless config file whose `llm:` section builds the judge (also how a bench-specific judge model is chosen) |
| `--no-trials` | off | skip the live trial write |
| `--trials-url` / `--trials-key-file` | the configured live instance | override the target instance |
| `--trials-project` | `seamless` | the project the trials are scoped to |

Order is deliberate: `results.json` lands on disk **first**, then the tables
print, then the trials are recorded. A run that already cost tokens must not be
lost because a live instance was down.

The report reads:

```
seambench report -- /tmp/seamless-bench/runs
  runs:      4 total: 4 graded, 0 failed to run, 0 ungradeable
  matrix:    1 scenario(s) x 2 condition(s) x 1 version(s)
  control:   vanilla (uplift = pass-rate - control pass-rate)
  smallest cell: n=2 graded run(s); one run moves that pass-rate by 0.50. Read anything smaller as noise.

=== version v0.4.5-34-g8719e35-dirty ===

scenario          condition  pass-rate   uplift  failed  ungradeable
cookie-hardening  vanilla    1.00 (2/2)  -       0       0
cookie-hardening  mechanism  0.00 (0/2)  -1.00   0       0

metrics (mean +- sd over graded runs)
scenario          condition  n  turns  inputTokens  outputTokens  costUsd  toolCalls
cookie-hardening  vanilla    2  9.00   52000        900.0         0.3100   0
cookie-hardening  mechanism  2  9.00   52000        900.0         0.3100   0

results: /tmp/seamless-bench/runs/results.json
```

(That is the shape of a fake-agent dry run -- the mechanism arm sits at zero
because a fake agent fires no hooks. Do not read it as a finding.)

`results.json` carries every run: its manifest, its verdict, its per-check
details, and both halves of its metrics. It is the portable artifact; the tables
are a rendering of it.

## 3. Version comparison

```bash
make seambench BASELINE=v0.4.5 N=5
# = go run ./cmd/seambench run --baseline v0.4.5 -n 5  &&  ... report
```

The whole suite runs twice. Each version's arms are built from **that version's
own checkout** -- its `harness.sh`, its `install-hooks`, its `seamlessd` -- under
its own base dir and port range, so the two never share a seeded data dir. The
baseline checkout is a detached `git worktree add` under the throwaway base,
removed on the way out. Captures nest under `<out>/<version>/`, and the pair is
recorded in `versions.json` before the runs start, so an interrupted comparison
still knows which label is which.

What is deliberately *shared*: the scenario fixture -- seed, prompt, grader -- is
the candidate's on both arms. It has to be; two versions graded against two
different experiments do not compare. The cost is that a baseline daemon opens a
data dir seeded by candidate-level store code, so `run` hard-errors when the
baseline's migration list is not a prefix of the candidate's, and prints the
extra migrations otherwise. Read that note: additive migrations are harmless
here, a destructive or renaming one is not, and it would surface as a plausible
uplift regression rather than as an error.

`--baseline` cannot be combined with `--reuse-arms`.

---

## Reading the numbers honestly

This suite is small-N by construction -- every run costs money -- so the report
is built to make its own uncertainty visible. Use it.

- **A pass-rate never travels without its counts.** `0.50 (1/2)` is a sentence
  about two runs. The header states the smallest cell in the matrix and how far
  one run moves it (`n=2` -> 0.50). Anything smaller than that step is noise;
  do not report it as a change.
- **There are three outcomes, not two.** `graded` (a real verdict),
  `failed to run` (the agent crashed, timed out, or the seed failed -- there is
  no verdict), and `ungradeable` (the run left no gradeable evidence). **Only
  graded runs enter a pass-rate.** A crashed run is never a failure, in the
  tables or in the recorded trials. The `failed` and `ungradeable` columns are
  there so a hole in the matrix is visible instead of implied -- a cell that is
  half infrastructure noise is not a measurement.
- **Uplift is a difference, not a score.** The control's own row shows `-`
  rather than `+0.00`: it is the reference, not a measurement of itself. Uplift
  needs exactly one arm with the `vanilla` profile; with none or several the
  column reads `n/a` and the header says why, instead of inventing a baseline.
- **The `ALL` row is a micro-average.** It pools graded runs across scenarios, so
  uneven cells weight by graded count. It appears only when there is more than
  one scenario.
- **Control drift qualifies everything above it.** The delta table is followed by
  the control's own pass-rate at each version. The `vanilla` arm has no Seamless
  in it, so if it moved, the model or the environment moved, and every delta in
  the table is a difference *on top of that drift*. A version comparison where
  the control swung is weak evidence no matter how clean the mechanism rows look.
- **Metrics carry their spread.** The metrics table is `mean +- sample sd` over
  the graded runs of each cell, never a bare mean. `n=1` prints the mean alone,
  which is the honest rendering of one observation.
- **Compare like with like.** If you change a grader, `--regrade` *both* versions
  before comparing them; a delta between two differently-graded trees measures
  the grader.
- **Do not read a single suite as a verdict on Seamless.** One matrix at `n=1`
  is an anecdote with a table around it. The number that matters is the trend
  across several runs at the same N.

---

## The one live write: trials

Every run in the report is recorded as a research-lab trial in the **live**
Seamless instance, in the lab `seambench`, project `seamless` by default. This is
the one deliberate exception to "nothing touches live state", and it is there
because a version-over-version uplift number is worth more in six months than the
run tree it came from -- and because Seamless's own trial primitive is where
durable expected-vs-actual records belong.

Three rules keep the exception safe, all enforced in code:

1. `results.json` is written **before** any trial.
2. An unreachable, unauthenticated, or refusing instance is a **loud warning and
   exit 0**, with the retry command printed. Nothing is lost.
3. Recording is **idempotent per run**: the trial id is stamped into the run's
   `grade.json`, so re-reporting the same tree records nothing twice.
   (`--regrade` deliberately produces fresh trials -- the verdict changed, so it
   is a new observation.)

The `{version, condition, scenario, run}` coordinates go into trial *metrics*
rather than the title, so they are exactly matchable later:

```
trial_query lab=seambench metrics_filter={"scenario":"cookie-hardening","condition":"mechanism"}
```

Outcome is `pass`, `fail`, or **`inconclusive`** -- a failed-to-run or ungradeable
run is never recorded as `fail`, the same rule the pass-rate follows.

**Skip it** with `--no-trials` (or `make seambench REPORT_ARGS=--no-trials`).
Always skip it for dry runs: fake-agent results in the live lab are how a future
reader mistakes a plumbing test for a measurement. Point it somewhere else with
`--trials-url` + `--trials-key-file`.

---

## The headless-recall constraint

Headless `claude -p` fires **SessionStart but not UserPromptSubmit** (memory
`headless-cc-p-skips-userpromptsubmit-hook`). Consequences:

- Everything the SessionStart briefing surfaces -- findings, plan lines, the
  memory index, the constraint tier -- lands in a `-p` take. That is the majority
  of the mechanism, and it is what the current scenarios measure.
- The passive mid-session `<seam-recall>` injection **never** lands. A scenario
  whose signal is that injection would silently measure the *absence* of a
  mechanism if it were run as a plain `-p` take.

So a scenario declares `RequiresRecall: true` and the runner takes a different
path for it: it POSTs the arm's own daemon the same `UserPromptSubmit` body
Claude Code would send and asserts a `<seam-recall>` block comes back
(`recall.go`). That is a component check recorded as an ordinary run artifact, so
the report and the grader see one uniform shape. A `vanilla` arm has no daemon,
so such a cell records as a failed run with the reason in it, rather than
vanishing from the matrix.

The alternative -- driving an interactive PTY, which does fire UserPromptSubmit --
is still on the table if a full-agent recall scenario is ever needed. It is
fragile: interactive Claude Code flushes its transcript only on a clean exit, and
the driver has to handle the permission dialog and any mid-task menus.

No scenario sets `RequiresRecall` today.

---

## Inside a run directory

The run directory is the *entire* handoff from runner to grader. Nothing else is
shared, and nothing reads a run's coordinates out of its path -- the manifest
carries them.

```
<out>/<scenario>/<condition>/run-01/     (a version comparison nests under <out>/<version>/)
  run.json          the manifest: scenario, condition, version, run index, model,
                    prompt, exit code, timings, and the runner's half of the metrics
                    (a multi-step run carries a per-session breakdown under "steps",
                    with the top-level metrics as the runner-half sum)
  grade.json        the verdict the report wrote back (absent until graded)
  diff.patch        git diff of the demo repo against the pre-run snapshot
  events.json       the arm's whole event log as a JSON array of core.Event
  transcript.jsonl  the agent transcript (absent if the agent left none)
  agent.log         the agent process's stdout + stderr
  repo/             preserved copy of the demo-repo working tree after the run
  data/             preserved copy of the arm's data dir (absent on a vanilla arm)
  steps/step-01/    a multi-step run's NON-final sessions, oldest first: each holds
                    its own agent.log, transcript.jsonl, and diff.patch; the
                    top-level artifacts are the final session's (absent otherwise)
```

For a multi-step run the graded tree is the FINAL session's, and the one event
log spans every session -- two SessionStart injections in a two-session run is
the truth, not a double-count. The judge reads every session's transcript in
order.

`data/` is copied **whole**, `-wal` and `-shm` included: the daemon runs in WAL
mode, so the tail of the event log -- the `session_end` finding, the last tool
calls, the task transitions -- can still be in the WAL, and copying `seam.db`
alone would drop exactly the evidence that the mechanism fired.

Metrics have two disjoint halves: the runner records what only the run knew
(turns, tokens, cost, duration) into `run.json`; the grader derives the rest
(tool calls and their names, injections, memory reads, memory writes, recalls
and recall misses, session findings, task transitions, mishaps, tool errors)
from the preserved artifacts into `grade.json`. `bench.MergeMetrics` recombines
them field-wise.

## The judge layer

`internal/bench` has an optional LLM-judge layer over the transcript, for the
fuzzy remainder that assertions cannot capture -- did the agent *explain* the
constraint it honored, is a two-session diagnosis actually correct. It **never
gates** -- it is additive commentary in `Details`, and its absence (no
provider, or an outage) degrades the run instead of failing it, per the
constraint `llm-degradation-remote-vs-local`.

Enable it with `report --judge`. The judge's provider and model come from the
Seamless config's `llm:` section (the standard search order, or an explicit
`--judge-config FILE` -- which is also how a bench-specific judge model is
chosen: a yaml, not a flag). Because the operator asked explicitly, a provider
that cannot be BUILT is a loud error; only per-run judge failures degrade. Two
things to know:

- Cached `grade.json` verdicts skip the judge: `--judge` alone only judges
  newly-graded runs. Pair it with `--regrade` to judge a tree that is already
  graded.
- Every scenario ships a rubric, so `--judge` adds one advisory line per run;
  without it each run reads `judge: n/a -- no LLM judge configured`.

---

## Adding a scenario

A scenario is `{Name, Prompt (or Steps), Seed, Grader, RequiresRecall}` in the
`internal/bench` table -- Go, not YAML, because a seed and a grader are code.
`Prompt` is sugar for a single agent session; a multi-session scenario sets
`Steps` instead (per-step prompt, optional runner-materialized `Evidence`
files, optional `FreshRepo` boundary), and setting both is a table error. The
shared seeding vocabulary -- the myapp project, the common noise-memory pool,
the session/plan/trial helpers -- lives in `internal/bench/seed.go`; fixtures
are **forked** from the terminal-scene specs in `internal/demokit`,
deliberately: those specs are branding surface that must stay stable while this
suite churns. The fork reuses demokit's seeding primitives and owns its data.

The grading discipline (what may gate, what may only be observed, and why you
must never string-match briefing layout) is in `AGENTS.md` under "Benchmark
scenarios and graders". Read it before writing a grader.

---

## Related

- `scripts/fixture/harness.sh` -- the shared fixture. `--mode bench` builds the
  arms and is the single owner of what an arm contains; `--mode record` builds
  the branding recording fixture from the same spine.
- `scripts/branding/README.md` -- the other half of that fixture: the landing
  page's terminal animations and console screenshots. Independent flow, nothing
  shared below the harness.
- `internal/bench/artifacts.go` -- the frozen on-disk contract described above.
- `internal/bench/grade.go` -- the gate-vs-observed rule, in the code that
  implements it.
- Memory `seambench-architecture` -- why the system is shaped this way.
- Memory `fixture-install-hooks-needs-home-override` -- why the harness runs
  `install-hooks` under the arm's own `HOME`.
