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

	r := &runner{
		base:       baseDir,
		out:        outDir,
		version:    version,
		scenarios:  scenarios,
		conditions: conditions,
		n:          *n,
		timeout:    *timeout,
		agent: agentOpts{
			command:        *agentCmd,
			permissionMode: *permission,
			extra:          agentArgs,
		},
		serve: execServe(filepath.Join(repoRoot, "bin", "seamlessd")),
		w:     os.Stdout,
	}
	if !*reuseArms {
		if err := buildArms(ctx, harnessOpts{
			repoRoot:   repoRoot,
			base:       baseDir,
			model:      *model,
			port:       *port,
			conditions: conditions,
			noBuild:    *noBuild,
			w:          r.w,
		}); err != nil {
			return err
		}
	}
	return r.run(ctx)
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
		Prompt:    sc.Prompt,
		StartedAt: time.Now().UTC(),
	}
	fmt.Fprintf(r.w, "\n==> %s / %s run %d/%d\n", sc.Name, cond.Name, i, r.n)

	outcome := r.execute(ctx, sc, a, dir)
	rec.EndedAt = time.Now().UTC()
	rec.ExitCode = outcome.exitCode
	rec.Metrics = outcome.metrics
	if outcome.err != nil {
		rec.Error = outcome.err.Error()
	}

	if err := capture(ctx, a, dir, rec.StartedAt, outcome.sessionID); err != nil {
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
// agent (or, for a recall-dependent scenario, the hook-level component check).
func (r *runner) execute(ctx context.Context, sc bench.Scenario, a *arm, dir string) runOutcome {
	if err := r.prepare(ctx, sc, a); err != nil {
		return runOutcome{exitCode: -1, err: err}
	}
	if a.seamless {
		stop, err := r.serve(ctx, a, filepath.Join(a.dir, "daemon.log"))
		if err != nil {
			return runOutcome{exitCode: -1, err: err}
		}
		// Stopping here -- before the caller captures -- is what lets the event
		// dump and the data/ copy read a cleanly closed database.
		defer stop()
	}

	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	logPath := filepath.Join(dir, bench.AgentLogFile)
	if sc.RequiresRecall {
		return recallCheck(runCtx, a, sc.Prompt, logPath)
	}
	return runAgent(runCtx, a, sc.Prompt, r.agent, logPath)
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
		if sc.Prompt == "" {
			return nil, fmt.Errorf("scenario %s has no prompt", sc.Name)
		}
		if sc.Seed == nil {
			return nil, fmt.Errorf("scenario %s has no seed", sc.Name)
		}
	}
	return out, nil
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
