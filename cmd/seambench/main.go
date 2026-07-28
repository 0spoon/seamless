// Command seambench drives the agent-scenario benchmark: for every scenario in
// internal/bench, under every named condition arm, N times, it re-seeds a
// throwaway Seamless instance, runs the agent headless in that arm's demo repo,
// and captures the run into the on-disk artifact contract
// (internal/bench/artifacts.go) that the grader and the report read back.
//
// Subcommands:
//
//	seambench run   build the arms, run the scenario x condition matrix, capture
//
// A `report` subcommand -- aggregate with-vs-without uplift and version deltas
// over the same run dirs -- lands with step 7 of plan:seambench; it plugs into
// the dispatch below and reads nothing but the artifact directories `run`
// writes. The run dir is the entire handoff: this command never calls a grader.
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
  seambench run [flags]     run the scenario x condition matrix and capture it

Run "seambench run -h" for the flags.
`)
}
