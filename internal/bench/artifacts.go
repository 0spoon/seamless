// The on-disk contract between the two halves of a benchmark run: cmd/seambench
// captures a run into an artifact directory, and the grader + report read it
// back. Nothing else is shared -- the runner never calls a grader inline, and a
// grader never reaches into a live arm -- so a run dir is the whole handoff.

package bench

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Artifact file names inside one run directory. A run directory is laid out:
//
//	<out>/<scenario>/<condition>/run-01/
//	  run.json          the RunRecord manifest
//	  grade.json        the Grade the report wrote back (absent until graded)
//	  diff.patch        git diff of the demo repo against the pre-run snapshot
//	  events.json       the arm's event log, dumped as a JSON array of core.Event
//	  transcript.jsonl  the agent transcript
//	  agent.log         the agent process's stdout+stderr
//	  repo/             preserved copy of the arm's demo-repo working tree
//	  data/             preserved copy of the arm's Seamless data dir (absent on vanilla)
//	  steps/step-01/    a multi-step run's NON-final sessions, oldest first:
//	                    each holds its own agent.log, transcript.jsonl, and
//	                    diff.patch; the top-level artifacts are the final
//	                    session's (absent on single-step runs)
//
// A version comparison nests the whole tree one level deeper, under the version
// label: <out>/<version>/<scenario>/<condition>/run-01/. Nothing reads a run's
// coordinates out of its path -- the manifest carries them -- so the extra level
// costs the readers nothing (RunDirs finds run dirs by manifest, not by depth).
const (
	RunManifestFile = "run.json"
	GradeFile       = "grade.json"
	DiffFile        = "diff.patch"
	EventsFile      = "events.json"
	TranscriptFile  = "transcript.jsonl"
	AgentLogFile    = "agent.log"
	RepoDirName     = "repo"
	DataDirName     = "data"
	StepsDirName    = "steps"
)

// StepDirName is the per-step artifact directory of a multi-step run's
// non-final session, 1-based: steps/step-01.
func StepDirName(i int) string {
	return filepath.Join(StepsDirName, fmt.Sprintf("step-%02d", i))
}

// Metrics is the per-run measurement set the report aggregates.
//
// The two halves fill disjoint groups: the runner records what only the run
// itself knows (agent cost and shape), the grader derives the rest from the
// preserved artifacts. Adding fields is fine; renaming or repurposing an
// existing one breaks the other half.
type Metrics struct {
	// Recorded by the runner, from the agent process.
	Turns        int     `json:"turns,omitempty"`
	InputTokens  int     `json:"inputTokens,omitempty"`
	OutputTokens int     `json:"outputTokens,omitempty"`
	CostUSD      float64 `json:"costUsd,omitempty"`
	DurationMS   int64   `json:"durationMs,omitempty"`

	// Derived by the grader, from the preserved event log and data dir.
	ToolCalls       int            `json:"toolCalls,omitempty"`
	ToolCallsByName map[string]int `json:"toolCallsByName,omitempty"`
	Injections      int            `json:"injections,omitempty"`
	MemoryReads     int            `json:"memoryReads,omitempty"`
	Recalls         int            `json:"recalls,omitempty"`
	RecallMisses    int            `json:"recallMisses,omitempty"`
	MemoryWrites    int            `json:"memoryWrites,omitempty"`
	SessionFindings int            `json:"sessionFindings,omitempty"`
	TaskTransitions int            `json:"taskTransitions,omitempty"`
	Mishaps         int            `json:"mishaps,omitempty"`
	ToolErrors      int            `json:"toolErrors,omitempty"`
}

// MergeMetrics recombines the two disjoint halves of a run's measurements: the
// runner's (recorded from the agent process, carried on the RunRecord) and the
// grader's (derived from the preserved artifacts, carried on the Grade). The
// split is by field, not by "whichever is non-zero", so a legitimately zero
// measurement -- an agent that made no tool calls -- stays zero instead of
// being back-filled from the other half.
func MergeMetrics(runner, grader Metrics) Metrics {
	m := grader
	m.Turns = runner.Turns
	m.InputTokens = runner.InputTokens
	m.OutputTokens = runner.OutputTokens
	m.CostUSD = runner.CostUSD
	m.DurationMS = runner.DurationMS
	return m
}

// SumRunnerMetrics sums the RUNNER-owned half over a multi-step run's
// sessions -- turns, tokens, cost, wall-clock -- for the manifest's top-level
// Metrics. The grader half is left zero: it is derived once from the whole
// run's preserved event log, never summed per step (MergeMetrics recombines
// the two halves field-wise later, same as on a single-step run).
func SumRunnerMetrics(ms ...Metrics) Metrics {
	var out Metrics
	for _, m := range ms {
		out.Turns += m.Turns
		out.InputTokens += m.InputTokens
		out.OutputTokens += m.OutputTokens
		out.CostUSD += m.CostUSD
		out.DurationMS += m.DurationMS
	}
	return out
}

// Fields renders the scalar metrics as name -> value for aggregation and for
// trial metrics, keyed by the JSON field names so the report, results.json, and
// a recorded trial all speak one vocabulary. Derived by reflection rather than
// transcribed: a hand-kept list is exactly the kind that drifts one field
// behind the struct. ToolCallsByName is not scalar and is left out.
func (m Metrics) Fields() map[string]float64 {
	v := reflect.ValueOf(m)
	t := v.Type()
	out := make(map[string]float64, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		switch f := v.Field(i); f.Kind() {
		case reflect.Int, reflect.Int64:
			out[name] = float64(f.Int())
		case reflect.Float64:
			out[name] = f.Float()
		}
	}
	return out
}

// MetricNames is every scalar metric name, sorted -- the canonical vocabulary
// Fields produces.
func MetricNames() []string {
	names := slices.Collect(maps.Keys(Metrics{}.Fields()))
	slices.Sort(names)
	return names
}

// ReportedMetrics is the subset the terminal table shows. Everything else is
// still in results.json and in the recorded trials; this is a width budget, not
// a claim about what matters (a test keeps every name here real).
var ReportedMetrics = []string{"turns", "inputTokens", "outputTokens", "costUsd", "toolCalls"}

// RunRecord is the manifest one completed run leaves in its artifact
// directory. It carries everything the report needs to place the run in the
// matrix (scenario x condition x version x run index) without re-deriving it
// from directory names.
type RunRecord struct {
	Scenario  string    `json:"scenario"`
	Condition Condition `json:"condition"`
	Run       int       `json:"run"` // 1-based index within the scenario x condition cell
	Version   string    `json:"version"`
	Model     string    `json:"model,omitempty"`
	Prompt    string    `json:"prompt,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	ExitCode  int       `json:"exitCode"`
	// Error is non-empty when the run itself failed (agent crash, timeout,
	// harness error) as opposed to the agent simply doing the wrong thing.
	// A failed run is not a graded failure; the report counts it separately.
	Error   string  `json:"error,omitempty"`
	Metrics Metrics `json:"metrics"`
	// Concurrent records that this run shared the machine with the other
	// conditions of its scenario (`run --parallel-conditions`). Turns, tokens
	// and cost are unaffected by that; WALL-CLOCK IS. A concurrent run's
	// durationMs carries contention from two other agent sessions and their
	// daemons, so it is not comparable with a serial run's -- including across
	// the two halves of a version comparison, where a mode difference would
	// read as a timing regression. Absent on serial runs, whose manifests are
	// unchanged.
	Concurrent bool `json:"concurrent,omitempty"`
	// Steps is a multi-step run's per-session breakdown, in run order, holding
	// every session that was ATTEMPTED (a step the run never reached is not
	// recorded). Prompt is empty on such a run -- the prompts live on the
	// steps -- and the top-level Metrics is the runner-half sum over them
	// (SumRunnerMetrics), so every existing reader of run.json keeps working.
	// Absent on single-step runs, whose manifests are unchanged.
	Steps []StepRecord `json:"steps,omitempty"`
}

// StepRecord is one agent session's slice of a multi-step run's manifest.
type StepRecord struct {
	Name      string    `json:"name"`
	Prompt    string    `json:"prompt"`
	SessionID string    `json:"sessionId,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	ExitCode  int       `json:"exitCode"`
	Error     string    `json:"error,omitempty"`
	// Metrics is the runner-owned half for this session alone; the grader half
	// spans the whole run (one preserved event log covers every session).
	Metrics Metrics `json:"metrics"`
}

// WriteRunRecord writes the manifest into a run directory.
func WriteRunRecord(dir string, rec RunRecord) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("bench: marshal run record: %w", err)
	}
	path := filepath.Join(dir, RunManifestFile)
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("bench: write run record: %w", err)
	}
	return nil
}

// ReadRunRecord reads the manifest from a run directory.
func ReadRunRecord(dir string) (RunRecord, error) {
	b, err := os.ReadFile(filepath.Join(dir, RunManifestFile))
	if err != nil {
		return RunRecord{}, fmt.Errorf("bench: read run record: %w", err)
	}
	var rec RunRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return RunRecord{}, fmt.Errorf("bench: parse run record in %s: %w", dir, err)
	}
	return rec, nil
}

// LoadRun reads a run directory back into its manifest plus the artifact
// handle a grader takes. Optional artifacts that the run never produced
// (a vanilla arm has no data dir; a crashed agent may leave no transcript)
// come back as empty paths rather than errors -- graders decide what a
// missing artifact means for their checks.
func LoadRun(dir string) (RunRecord, RunArtifacts, error) {
	rec, err := ReadRunRecord(dir)
	if err != nil {
		return RunRecord{}, RunArtifacts{}, err
	}
	a := RunArtifacts{
		Scenario:  rec.Scenario,
		Condition: rec.Condition,
		Dir:       dir,
		RepoDir:   existingPath(filepath.Join(dir, RepoDirName)),
		DataDir:   existingPath(filepath.Join(dir, DataDirName)),
	}
	if p := existingPath(filepath.Join(dir, TranscriptFile)); p != "" {
		a.Transcript = p
	}
	if b, err := os.ReadFile(filepath.Join(dir, DiffFile)); err == nil {
		a.RepoDiff = string(b)
	} else if !os.IsNotExist(err) {
		return rec, RunArtifacts{}, fmt.Errorf("bench: read run diff in %s: %w", dir, err)
	}
	// A multi-step run's non-final sessions: the last StepRecord's artifacts
	// are the top-level ones, so only its predecessors live under steps/.
	// Missing per-step artifacts come back empty, same tolerance as above.
	for i, st := range rec.Steps {
		if i == len(rec.Steps)-1 {
			break
		}
		stepDir := filepath.Join(dir, StepDirName(i+1))
		sa := StepArtifacts{
			Name:       st.Name,
			Transcript: existingPath(filepath.Join(stepDir, TranscriptFile)),
		}
		if b, err := os.ReadFile(filepath.Join(stepDir, DiffFile)); err == nil {
			sa.RepoDiff = string(b)
		} else if !os.IsNotExist(err) {
			return rec, RunArtifacts{}, fmt.Errorf("bench: read step %d diff in %s: %w", i+1, dir, err)
		}
		a.Steps = append(a.Steps, sa)
	}
	return rec, a, nil
}

func existingPath(p string) string {
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}
