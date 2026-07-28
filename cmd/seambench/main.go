// Command seambench drives the agent-scenario benchmark: for every scenario in
// internal/bench, under every named condition arm, N times, it re-seeds a
// throwaway Seamless instance, runs the agent headless in that arm's demo repo,
// and captures the run into the on-disk artifact contract
// (internal/bench/artifacts.go) that the grader and the report read back.
//
// Subcommands:
//
//	seambench run      build the arms, run the scenario x condition matrix, capture
//	seambench report   grade the captured runs, export results.json, print the
//	                   with-vs-without uplift and the baseline-vs-candidate delta
//
// The two halves meet only on disk. `run` writes run directories and grades
// nothing; `report` reads them and runs nothing. That split is what makes a
// grader fix re-appliable to runs that already cost their tokens
// (`report --regrade`), and what lets a run tree be graded on another machine.
//
// This is the AGENT-SCENARIO benchmark (make seambench), not the Go hot-path
// micro-benchmarks behind `make bench`. Keep the two distinct.
//
// Nothing here touches live state. scripts/fixture/harness.sh --mode bench
// builds every arm under a throwaway base dir with its own home, demo repo,
// data dir, config, key, and non-live port; this command only ever serves and
// seeds those, and never runs install-hooks itself.
package main

import (
	"fmt"
	"log/slog"
	"os"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "run":
		err = runRun(args)
	case "report":
		err = runReport(args, os.Stdout)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "seambench: unknown subcommand %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "seambench: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `seambench -- the Seamless agent-scenario benchmark

Usage:
  seambench run [flags]      run the scenario x condition matrix and capture it
                             (--baseline REF also runs it against that ref)
  seambench report [flags]   grade the captured runs and print uplift + version deltas

Run "seambench <cmd> -h" for the flags.
`)
}
