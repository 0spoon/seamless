package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeVersionCheckReportsSurfaceVersionAndPath(t *testing.T) {
	candidate := runtimeCandidate{name: "codex app runtime", path: "/Applications/Codex.app/runtime"}
	chk := runtimeVersionCheck(candidate, func(_ context.Context, path string) (string, error) {
		require.Equal(t, candidate.path, path)
		return "codex-cli 0.145.0-alpha.18", nil
	})

	require.Equal(t, statusOK, chk.status)
	require.Equal(t, candidate.name, chk.name)
	require.Equal(t, "codex-cli 0.145.0-alpha.18 (/Applications/Codex.app/runtime)", chk.detail)
}

func TestRuntimeVersionCheckSurfacesInspectionFailure(t *testing.T) {
	candidate := runtimeCandidate{name: "codex CLI runtime", path: "/opt/codex"}
	chk := runtimeVersionCheck(candidate, func(context.Context, string) (string, error) {
		return "", errors.New("synthetic failure")
	})

	require.Equal(t, statusWarn, chk.status)
	require.Equal(t, candidate.name, chk.name)
	require.Contains(t, chk.detail, `cannot run "/opt/codex" --version`)
	require.Contains(t, chk.detail, "synthetic failure")
}
