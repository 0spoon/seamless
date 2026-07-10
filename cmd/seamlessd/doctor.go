package main

import (
	"flag"
	"fmt"
)

// checkStatus is the outcome of a single doctor check.
type checkStatus int

const (
	statusOK checkStatus = iota
	statusWarn
	statusFail
)

func (s checkStatus) label() string {
	switch s {
	case statusOK:
		return "ok"
	case statusWarn:
		return "warn"
	default:
		return "fail"
	}
}

// check is one line of the doctor report.
type check struct {
	status checkStatus
	name   string
	detail string
}

// doctor runs environment self-checks and prints a report. It exits non-zero
// (via a returned error) only when a check FAILs; warnings do not fail the run.
//
// P0 grows this: config loading and database reachability are added in later
// steps so the phase-0 acceptance ("doctor reports config + DB ok") is met.
func doctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	checks := []check{
		{statusOK, "binary", fmt.Sprintf("seamlessd %s runs", version)},
	}

	return reportChecks(checks)
}

// reportChecks prints each check and returns an error if any FAILed.
func reportChecks(checks []check) error {
	var failed int
	for _, c := range checks {
		fmt.Printf("  [%-4s] %s: %s\n", c.status.label(), c.name, c.detail)
		if c.status == statusFail {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("doctor: %d check(s) failed", failed)
	}
	return nil
}
