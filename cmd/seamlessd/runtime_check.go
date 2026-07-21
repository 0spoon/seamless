package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// runtimeCandidate is one discoverable agent-client runtime (a PATH CLI or a
// desktop app's bundled binary) to be version-probed independently.
type runtimeCandidate struct {
	name string
	path string
}

type runtimeVersionReader func(context.Context, string) (string, error)

// runtimeVersionCheck probes one candidate's self-reported version. A probe
// failure is a warning, not a failure: agent clients are optional, and the
// operator still learns which path could not be run.
func runtimeVersionCheck(candidate runtimeCandidate, readVersion runtimeVersionReader) check {
	ctx, cancel := context.WithTimeout(context.Background(), mcpCommandTimeout)
	defer cancel()

	version, err := readVersion(ctx, candidate.path)
	if err != nil {
		return check{statusWarn, candidate.name,
			fmt.Sprintf("cannot run %q --version: %v", candidate.path, err)}
	}
	return check{statusOK, candidate.name, fmt.Sprintf("%s (%s)", version, candidate.path)}
}

func readRuntimeVersion(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, path, "--version")
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if detail := strings.TrimSpace(string(exitErr.Stderr)); detail != "" {
				return "", fmt.Errorf("%w: %s", err, detail)
			}
		}
		return "", err
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return "", errors.New("empty version output")
	}
	return version, nil
}
