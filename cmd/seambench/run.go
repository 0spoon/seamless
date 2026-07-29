// `seambench run`: the headless runner.
//
// The arms are built ONCE (the harness copies a demo repo, seeds an instance,
// and installs hooks per arm -- expensive and identical across runs). Each
// individual run then re-establishes the starting state itself: the demo repo
// is reset to the commit captured right after the arm was built, and a
// Seamless-ful arm's data dir is wiped and re-seeded from the scenario's own
// seedFn (which replaces the harness's `demoseed -scenes` seed for bench arms).
// So run N+1 sees exactly what run N saw -- no drifting leases, no aged
// findings, no leftover edits.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/0spoon/seamless/internal/bench"
	"github.com/0spoon/seamless/internal/demokit"
)

// defaultTimeout is the per-run wall-clock budget for the agent.
const defaultTimeout = 15 * time.Minute

// defaultPort is the first port the harness hands to a Seamless-ful arm. It
// matches the harness default and is deliberately nowhere near the live 8081.
const defaultPort = 8099

func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: seambench run [flags]\n\n"+
			"Runs every selected scenario under every selected condition arm n times,\n"+
			"capturing each run into <out>/<scenario>/<condition>/run-NN/.\n\n"+
			"With --baseline REF the whole suite runs twice -- once against a throwaway\n"+
			"checkout of REF built from its own ref, once against the working tree -- and\n"+
			"each version's captures nest under <out>/<version>/.\n\n"+
			"Nothing is graded here: `seambench report` grades the captured run dirs.\n\n"+
			"Known scenarios: %s\n\nFlags:\n", strings.Join(scenarioNames(), ", "))
		fs.PrintDefaults()
	}
	var (
		scenarioList  = fs.String("scenario", "", "comma-separated scenarios to run (default: all)")
		conditionList = fs.String("conditions", defaultConditionList(), "comma-separated condition arms: name[:profile[:client]]")
		n             = fs.Int("n", 1, "runs per scenario x condition cell")
		base          = fs.String("base", defaultBase(), "throwaway base dir the harness builds the arms under")
		out           = fs.String("out", "", "artifact root for the captured runs (default <base>/runs)")
		model         = fs.String("model", "", "pin the agent model in every arm (default: the harness default; ignored with --reuse-arms)")
		port          = fs.Int("port", defaultPort, "first port for the Seamless-ful arms; each takes the next")
		timeout       = fs.Duration("timeout", defaultTimeout, "per-run wall-clock budget for the agent")
		versionLabel  = fs.String("version", "", "version label recorded in every run.json (default: git describe of the repo)")
		repoFlag      = fs.String("repo", "", "Seamless repo root holding "+harnessScript+" and bin/ (default: the git top-level of the cwd)")
		reuseArms     = fs.Bool("reuse-arms", false, "reuse the arms already built under --base instead of rebuilding them")
		noBuild       = fs.Bool("no-build", false, "pass --no-build to the harness (reuse the existing bin/)")
		agentCmd      = fs.String("agent-cmd", "claude", "agent CLI to run headless; the injection point for dry runs")
		permission    = fs.String("permission-mode", "bypassPermissions", "value for the agent's --permission-mode flag (empty omits it)")
		baselineRef   = fs.String("baseline", "", "also run the whole suite against this git ref, built from its own checkout, for a version comparison")
		credsMode     = fs.String("credentials", string(credAuto), "how an arm gets the agent's credentials: "+joinModes())
	)
	var agentArgs stringList
	fs.Var(&agentArgs, "agent-arg", "extra argument appended to the agent command (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *n < 1 {
		return fmt.Errorf("-n must be at least 1, got %d", *n)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conditions, err := bench.ParseConditions(*conditionList)
	if err != nil {
		return err
	}
	scenarios, err := selectScenarios(*scenarioList)
	if err != nil {
		return err
	}
	repoRoot, err := resolveRepoRoot(ctx, *repoFlag)
	if err != nil {
		return err
	}
	baseDir, err := filepath.Abs(*base)
	if err != nil {
		return fmt.Errorf("resolve --base %s: %w", *base, err)
	}
	outDir := *out
	if outDir == "" {
		outDir = filepath.Join(baseDir, "runs")
	}
	if outDir, err = filepath.Abs(outDir); err != nil {
		return fmt.Errorf("resolve --out %s: %w", *out, err)
	}
	version := *versionLabel
	if version == "" {
		version = repoVersion(ctx, repoRoot)
	}
	mode, err := parseCredentialMode(*credsMode)
	if err != nil {
		return err
	}
	creds, why, err := newCredentials(ctx, mode)
	if err != nil {
		return err
	}
	if why != "" {
		fmt.Fprintf(os.Stdout, "==> credentials: %s (%s)\n", creds.mode, why)
	}

	s := &suite{
		out:        outDir,
		scenarios:  scenarios,
		conditions: conditions,
		n:          *n,
		timeout:    *timeout,
		model:      *model,
		reuseArms:  *reuseArms,
		noBuild:    *noBuild,
		agent: agentOpts{
			command:        *agentCmd,
			permissionMode: *permission,
			extra:          agentArgs,
		},
		creds: creds,
		w:     os.Stdout,
	}
	candidate := versionArm{
		role: "candidate", label: version, repoRoot: repoRoot, base: baseDir, port: *port,
	}
	if *baselineRef == "" {
		return s.run(ctx, candidate)
	}
	return s.compare(ctx, candidate, *baselineRef)
}

// suite is one `run` invocation's configuration, minus the per-version parts.
type suite struct {
	out        string
	scenarios  []bench.Scenario
	conditions []bench.Condition
	n          int
	timeout    time.Duration
	model      string
	reuseArms  bool
	noBuild    bool
	agent      agentOpts
	creds      *credentials
	w          io.Writer
}

// versionArm is one version under test: whose code builds the arms, what the
// runs are labelled, and which throwaway base and port range they get. Two
// versions never share a base dir, so they never share a seeded data dir
// either (see version.go for why that is load-bearing rather than tidy).
type versionArm struct {
	role     string // "candidate" or "baseline", for the log line only
	label    string
	repoRoot string
	base     string
	port     int
	// outSub nests this version's captures under <out>/<slug>/. Empty keeps the
	// single-version layout, which is what a plain `run` still writes.
	outSub string
}

// versionPortStride keeps a second version's arms clear of the first's. The two
// run sequentially, so this is belt-and-braces against a daemon that outlives
// its stop.
const versionPortStride = 20

// run builds one version's arms and walks the matrix.
func (s *suite) run(ctx context.Context, v versionArm) error {
	out := s.out
	if v.outSub != "" {
		out = filepath.Join(s.out, v.outSub)
	}
	r := &runner{
		base:       v.base,
		out:        out,
		version:    v.label,
		scenarios:  s.scenarios,
		conditions: s.conditions,
		n:          s.n,
		timeout:    s.timeout,
		agent:      s.agent,
		creds:      s.creds,
		serve:      execServe(filepath.Join(v.repoRoot, "bin", "seamlessd")),
		w:          s.w,
	}
	if !s.reuseArms {
		if err := buildArms(ctx, harnessOpts{
			repoRoot:   v.repoRoot,
			base:       v.base,
			model:      s.model,
			port:       v.port,
			conditions: s.conditions,
			// A baseline checkout has no bin/ yet, so its build is not optional.
			noBuild: s.noBuild && v.role == "candidate",
			w:       s.w,
		}); err != nil {
			return err
		}
	}
	fmt.Fprintf(s.w, "\n===> %s version %s (from %s)\n", v.role, v.label, v.repoRoot)
	return r.run(ctx)
}

// compare runs the suite twice: once against a throwaway checkout of the
// baseline ref, once against the working tree. Each version's arms are built
// from its own ref, under its own base dir -- see version.go.
func (s *suite) compare(ctx context.Context, candidate versionArm, ref string) error {
	if s.reuseArms {
		return fmt.Errorf("--reuse-arms cannot be combined with --baseline: " +
			"a version comparison builds each version's arms from its own ref")
	}

	src, err := addBaselineWorktree(ctx, candidate.repoRoot,
		ref, filepath.Join(candidate.base, "baseline-src"))
	if err != nil {
		return err
	}
	defer src.remove(ctx)

	if err := s.checkMigrationSkew(candidate.repoRoot, src.dir); err != nil {
		return err
	}

	baseline := versionArm{
		role:     "baseline",
		label:    repoVersion(ctx, src.dir),
		repoRoot: src.dir,
		base:     filepath.Join(candidate.base, "baseline"),
		port:     candidate.port + versionPortStride,
	}
	if baseline.label == candidate.label {
		return fmt.Errorf("the baseline ref %q and the working tree are both %q: "+
			"there is nothing to compare (pass --version to label them apart if that is intended)",
			ref, baseline.label)
	}
	baseline.outSub = versionSlug(baseline.label)
	candidate.outSub = versionSlug(candidate.label)
	candidate.base = filepath.Join(candidate.base, "candidate")

	// Recorded before the runs so an interrupted comparison still says which
	// way round the two labels go.
	if err := bench.WriteVersionPair(s.out, bench.VersionPair{
		Baseline: baseline.label, Candidate: candidate.label,
	}); err != nil {
		return err
	}
	if err := s.run(ctx, baseline); err != nil {
		return fmt.Errorf("baseline %s: %w", baseline.label, err)
	}
	if err := s.run(ctx, candidate); err != nil {
		return fmt.Errorf("candidate %s: %w", candidate.label, err)
	}
	fmt.Fprintf(s.w, "\nversion comparison captured: baseline %s, candidate %s\n"+
		"Report it with: seambench report --out %s\n", baseline.label, candidate.label, s.out)
	return nil
}

// checkMigrationSkew refuses a comparison whose two refs disagree about
// already-applied schema history, and names the migrations the candidate has
// beyond the baseline so an operator can see what the baseline daemon will meet
// in a candidate-seeded data dir.
func (s *suite) checkMigrationSkew(candidateRoot, baselineRoot string) error {
	cand, err := migrationNames(candidateRoot)
	if err != nil {
		return err
	}
	base, err := migrationNames(baselineRoot)
	if err != nil {
		return err
	}
	extra, err := migrationSkew(base, cand)
	if err != nil {
		return fmt.Errorf("baseline/candidate schema mismatch: %w", err)
	}
	if len(extra) == 0 {
		return nil
	}
	fmt.Fprintf(s.w, "\n==> NOTE: the candidate has %d migration(s) the baseline does not:\n      %s\n"+
		"    Both arms are seeded by the candidate's store code (the fixture must be identical\n"+
		"    on both sides), so the baseline daemon opens a data dir already migrated past its\n"+
		"    own list. That is harmless for additive migrations and NOT harmless for a\n"+
		"    destructive or renaming one -- check the SQL above before trusting the delta.\n",
		len(extra), strings.Join(extra, "\n      "))
	return nil
}

// runner holds one invocation's resolved configuration and the arms it drives.
type runner struct {
	base       string
	out        string
	version    string
	scenarios  []bench.Scenario
	conditions []bench.Condition
	arms       map[string]*arm
	n          int
	timeout    time.Duration
	agent      agentOpts
	// creds provisions each arm with the agent's credentials for the duration
	// of a run; nil means provision nothing (what the fake-agent tests use).
	creds *credentials
	// serve starts an arm's daemon; a field so a dry run can substitute one.
	serve serveFunc
	w     io.Writer
}

// run walks the scenario x condition x N matrix.
func (r *runner) run(ctx context.Context) error {
	if err := r.loadArms(ctx); err != nil {
		return err
	}
	total, failed := 0, 0
	for _, sc := range r.scenarios {
		for _, cond := range r.conditions {
			for i := 1; i <= r.n; i++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				rec, dir, err := r.runOne(ctx, sc, cond, i)
				if err != nil {
					return err
				}
				total++
				if rec.Error != "" {
					failed++
				}
				r.report(rec, dir)
			}
		}
	}
	fmt.Fprintf(r.w, "\n%d run(s), %d failed. Artifacts under %s\n", total, failed, r.out)
	if failed == total {
		return fmt.Errorf("every run failed; see the run records under %s", r.out)
	}
	return nil
}

// loadArms reads each condition's env file and records the demo-repo commit
// every run of that arm resets to.
func (r *runner) loadArms(ctx context.Context) error {
	r.arms = make(map[string]*arm, len(r.conditions))
	for _, cond := range r.conditions {
		a, err := loadArm(armEnvPath(r.base, cond.Name), cond)
		if err != nil {
			return err
		}
		if a.snapshot, err = gitSnapshot(ctx, a.repo); err != nil {
			return err
		}
		r.arms[cond.Name] = a
	}
	return nil
}

// runOne performs and captures a single run. The returned error means the run
// could not be RECORDED (the artifact dir or its manifest is unwritable) and
// aborts the suite; everything else -- a timeout, a crashed agent, a failed
// seed -- lands in the run record's Error and the suite moves on.
func (r *runner) runOne(ctx context.Context, sc bench.Scenario, cond bench.Condition, i int) (bench.RunRecord, string, error) {
	a := r.arms[cond.Name]
	dir := filepath.Join(r.out, sc.Name, cond.Name, fmt.Sprintf("run-%02d", i))
	// A re-run of the same cell replaces it rather than merging into a
	// half-stale directory.
	if err := os.RemoveAll(dir); err != nil {
		return bench.RunRecord{}, dir, fmt.Errorf("clear run dir %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return bench.RunRecord{}, dir, fmt.Errorf("create run dir %s: %w", dir, err)
	}

	rec := bench.RunRecord{
		Scenario:  sc.Name,
		Condition: cond,
		Run:       i,
		Version:   r.version,
		Model:     a.model,
		StartedAt: time.Now().UTC(),
	}
	if len(sc.Sessions()) == 1 {
		// A multi-step run's prompts live on its StepRecords instead.
		rec.Prompt = sc.Sessions()[0].Prompt
	}
	fmt.Fprintf(r.w, "\n==> %s / %s run %d/%d\n", sc.Name, cond.Name, i, r.n)

	outcome := r.execute(ctx, sc, a, dir)
	rec.EndedAt = time.Now().UTC()
	rec.ExitCode = outcome.exitCode
	rec.Metrics = outcome.metrics
	if outcome.err != nil {
		rec.Error = outcome.err.Error()
	}
	if outcome.planned > 1 {
		rec.Steps = stepRecords(outcome.steps)
	}

	if err := capture(ctx, a, dir, outcome); err != nil {
		rec.Error = joinMessages(rec.Error, err.Error())
	}
	if err := bench.WriteRunRecord(dir, rec); err != nil {
		return rec, dir, err
	}
	// The run dir is the whole handoff to the grader, so prove it reads back
	// before moving on rather than at report time, hours and many runs later.
	if _, _, err := bench.LoadRun(dir); err != nil {
		return rec, dir, fmt.Errorf("captured run %s does not load back: %w", dir, err)
	}
	return rec, dir, nil
}

// execute prepares the arm, holds its daemon up for the duration, and runs the
// scenario's agent sessions in order (or, for a recall-dependent scenario, the
// hook-level component check). The daemon and the credential wrap the WHOLE
// session sequence: a handoff scenario's sessions share one live instance,
// which is precisely the persistence being measured.
func (r *runner) execute(ctx context.Context, sc bench.Scenario, a *arm, dir string) runOutcome {
	if err := r.prepare(ctx, sc, a); err != nil {
		return failedRun(err)
	}
	if a.seamless {
		stop, err := r.serve(ctx, a, filepath.Join(a.dir, "daemon.log"))
		if err != nil {
			return failedRun(err)
		}
		// Stopping here -- before the caller captures -- is what lets the event
		// dump and the data/ copy read a cleanly closed database.
		defer stop()
	}

	// The credential lives in the arm for this run only, and is taken back out
	// before the caller captures -- capture copies the arm's repo, data dir and
	// transcript, never its config dir, but the narrow window is the point.
	if r.creds != nil {
		release, err := r.creds.provision(a)
		if err != nil {
			return failedRun(err)
		}
		defer release()
	}

	if sc.RequiresRecall {
		runCtx, cancel := context.WithTimeout(ctx, r.timeout)
		defer cancel()
		so := recallCheck(runCtx, a, sc.Prompt, filepath.Join(dir, bench.AgentLogFile))
		return finishRun(1, []stepOutcome{so})
	}

	agent := r.agent
	if r.creds != nil {
		agent.env = r.creds.env()
	}
	steps := sc.Sessions()
	outcomes := make([]stepOutcome, 0, len(steps))
	for i, st := range steps {
		so := r.executeStep(ctx, a, dir, st, i, len(steps), agent)
		outcomes = append(outcomes, so)
		if so.err != nil {
			break // an aborted run: the remaining sessions never happen
		}
	}
	return finishRun(len(steps), outcomes)
}

// executeStep runs one of a scenario's agent sessions: the fresh-repo boundary
// and evidence materialization before it, the evidence removal and (for a
// non-final step) the step diff after it. --timeout bounds each session.
func (r *runner) executeStep(ctx context.Context, a *arm, dir string, st bench.Step, i, total int, agent agentOpts) stepOutcome {
	name := st.Name
	if name == "" {
		name = fmt.Sprintf("step-%02d", i+1)
	}
	fail := func(err error) stepOutcome {
		return stepOutcome{name: name, prompt: st.Prompt, exitCode: -1, err: err}
	}
	final := i == total-1

	// The boundary: a fresh-repo step starts from the arm snapshot, so nothing
	// a previous session left in the WORKING TREE carries over. The data dir
	// is deliberately untouched -- what a session recorded there is the only
	// channel across the boundary, and on a vanilla arm there is none.
	if i > 0 && st.FreshRepo {
		if err := gitRestore(ctx, a.repo, a.snapshot); err != nil {
			return fail(err)
		}
	}
	removeEvidence, err := writeEvidence(a.repo, st.Evidence)
	if err != nil {
		return fail(err)
	}

	logPath := filepath.Join(dir, bench.AgentLogFile)
	if !final {
		stepDir := filepath.Join(dir, bench.StepDirName(i+1))
		if err := os.MkdirAll(stepDir, 0o755); err != nil {
			err = fmt.Errorf("create step dir %s: %w", stepDir, err)
			if rerr := removeEvidence(); rerr != nil {
				err = errors.Join(err, rerr)
			}
			return fail(err)
		}
		logPath = filepath.Join(stepDir, bench.AgentLogFile)
	}

	stepCtx, cancel := context.WithTimeout(ctx, r.timeout)
	so := runAgent(stepCtx, a, st.Prompt, agent, logPath)
	cancel()
	so.name = name

	// Evidence comes out UNCONDITIONALLY -- error paths included -- before any
	// diff or capture can see it: gitDiff stages untracked files, so evidence
	// left behind would read as agent work on every arm.
	if err := removeEvidence(); err != nil && so.err == nil {
		so.err = err
	}
	if !final && so.err == nil {
		// This state is destroyed by the next boundary (or left to accumulate
		// under a carry-over step), so the step's diff is taken now.
		if so.repoDiff, err = gitDiff(ctx, a.repo, a.snapshot); err != nil {
			so.err = err
		} else if err := gitUnstage(ctx, a.repo); err != nil {
			// Drop the intent-to-add entries gitDiff staged, so a carry-over
			// successor session does not see phantom staged files.
			so.err = err
		}
	}
	return so
}

// stepRecords renders attempted step outcomes for the run manifest.
func stepRecords(steps []stepOutcome) []bench.StepRecord {
	out := make([]bench.StepRecord, len(steps))
	for i, so := range steps {
		out[i] = bench.StepRecord{
			Name:      so.name,
			Prompt:    so.prompt,
			SessionID: so.sessionID,
			StartedAt: so.startedAt,
			EndedAt:   so.endedAt,
			ExitCode:  so.exitCode,
			Metrics:   so.metrics,
		}
		if so.err != nil {
			out[i].Error = so.err.Error()
		}
	}
	return out
}

// prepare puts the arm back to the scenario's starting state.
func (r *runner) prepare(ctx context.Context, sc bench.Scenario, a *arm) error {
	if err := gitRestore(ctx, a.repo, a.snapshot); err != nil {
		return err
	}
	if !a.seamless {
		return nil // a vanilla arm has no instance to seed
	}
	if err := os.RemoveAll(a.dataDir); err != nil {
		return fmt.Errorf("wipe arm data dir %s: %w", a.dataDir, err)
	}
	s, err := demokit.New(a.dataDir)
	if err != nil {
		return fmt.Errorf("open a fresh data dir for %s: %w", a.condition.Name, err)
	}
	if err := sc.Seed(s, a.repo); err != nil {
		_ = s.Close()
		return fmt.Errorf("seed scenario %s into %s: %w", sc.Name, a.condition.Name, err)
	}
	if err := s.Close(); err != nil {
		return fmt.Errorf("close the seeded data dir for %s: %w", a.condition.Name, err)
	}
	return nil
}

// report prints one run's outcome.
func (r *runner) report(rec bench.RunRecord, dir string) {
	took := rec.EndedAt.Sub(rec.StartedAt).Round(time.Second)
	status := "ok"
	if rec.Error != "" {
		status = "FAILED: " + rec.Error
	}
	fmt.Fprintf(r.w, "    %s in %s (%d turns, %d in / %d out tokens, $%.4f)\n      %s\n",
		status, took, rec.Metrics.Turns, rec.Metrics.InputTokens, rec.Metrics.OutputTokens,
		rec.Metrics.CostUSD, dir)
}

// selectScenarios resolves the --scenario list against the bench table.
func selectScenarios(list string) ([]bench.Scenario, error) {
	out := bench.Scenarios()
	if strings.TrimSpace(list) != "" {
		out = nil
		seen := map[string]bool{}
		for name := range strings.SplitSeq(list, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			sc, ok := bench.ScenarioByName(name)
			if !ok {
				return nil, fmt.Errorf("unknown scenario %q: known scenarios are %s", name, strings.Join(scenarioNames(), ", "))
			}
			if seen[sc.Name] {
				return nil, fmt.Errorf("duplicate scenario %q", name)
			}
			seen[sc.Name] = true
			out = append(out, sc)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("scenario list %q selected nothing", list)
	}
	for _, sc := range out {
		if err := validateScenario(sc); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// validateScenario refuses a malformed table entry before any arm is touched.
// bench_test asserts the same shape; this is the runtime guard for a table
// edited without its test.
func validateScenario(sc bench.Scenario) error {
	if sc.Seed == nil {
		return fmt.Errorf("scenario %s has no seed", sc.Name)
	}
	if sc.Prompt != "" && len(sc.Steps) > 0 {
		return fmt.Errorf("scenario %s sets both Prompt and Steps; Prompt is sugar for a single step", sc.Name)
	}
	steps := sc.Sessions()
	if sc.RequiresRecall && len(steps) > 1 {
		return fmt.Errorf("scenario %s: RequiresRecall is a component check and cannot be multi-step", sc.Name)
	}
	for i, st := range steps {
		if st.Prompt == "" {
			return fmt.Errorf("scenario %s: step %d has no prompt", sc.Name, i+1)
		}
		for path := range st.Evidence {
			if !filepath.IsLocal(path) {
				return fmt.Errorf("scenario %s: step %d evidence path %q escapes the repo", sc.Name, i+1, path)
			}
		}
	}
	return nil
}

// scenarioNames lists the bench table, for help text and errors.
func scenarioNames() []string {
	all := bench.Scenarios()
	names := make([]string, len(all))
	for i, sc := range all {
		names[i] = sc.Name
	}
	return names
}

// defaultConditionList is the default --conditions value, derived from the
// canonical arm set rather than transcribed.
func defaultConditionList() string {
	conds := bench.DefaultConditions()
	names := make([]string, len(conds))
	for i, c := range conds {
		names[i] = c.Name
	}
	return strings.Join(names, ",")
}

// defaultBase mirrors the harness's own default base dir, so running either by
// hand lands in the same place.
func defaultBase() string { return filepath.Join(os.TempDir(), "seamless-bench") }

// joinMessages appends a second error message to a possibly-empty first.
func joinMessages(a, b string) string {
	if a == "" {
		return b
	}
	return errors.Join(errors.New(a), errors.New(b)).Error()
}

// stringList collects a repeatable string flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, " ") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}
