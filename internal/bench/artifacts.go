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
)

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
	return rec, a, nil
}

func existingPath(p string) string {
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}
