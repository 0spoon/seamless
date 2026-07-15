package main

import (
	"flag"
	"testing"

	"github.com/stretchr/testify/require"
)

// parseWith runs args through a flag set shaped like the real commands': one
// string flag, then a positional. It returns the flag's bound value plus
// whatever requireFlagsFirst made of the leftovers.
func parseWith(t *testing.T, args []string) (string, error) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(discardWriter{})
	project := fs.String("project", "", "project slug")
	require.NoError(t, fs.Parse(args))
	return *project, requireFlagsFirst(fs, "usage: test [--project P] URL")
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestRequireFlagsFirst_RejectsTrailingFlag pins the defect this guard exists
// for: Go's flag package stops at the first positional, so a trailing --project
// never binds. Before the guard the command ran anyway and filed to inbox.
func TestRequireFlagsFirst_RejectsTrailingFlag(t *testing.T) {
	project, err := parseWith(t, []string{"https://example.com", "--project", "myproj"})
	require.Empty(t, project, "the trailing flag does not bind -- that is the whole problem")
	require.Error(t, err)
	require.Contains(t, err.Error(), "flags must precede")
	require.Contains(t, err.Error(), "--project", "the error names the flag that was ignored")
}

func TestRequireFlagsFirst_AcceptsFlagsBeforePositional(t *testing.T) {
	project, err := parseWith(t, []string{"--project", "myproj", "https://example.com"})
	require.NoError(t, err)
	require.Equal(t, "myproj", project)
}

// Shorthand and --flag=value forms are equally silent when trailing, so they are
// equally rejected.
func TestRequireFlagsFirst_RejectsTrailingFlagForms(t *testing.T) {
	for _, form := range []string{"--project=myproj", "-project=myproj", "-project"} {
		_, err := parseWith(t, []string{"https://example.com", form})
		require.Error(t, err, form)
	}
}

func TestRequireFlagsFirst_AllowsPositionalOnly(t *testing.T) {
	_, err := parseWith(t, []string{"https://example.com"})
	require.NoError(t, err)
}

// The guard is safe only because no positional these commands take can start
// with "-": URLs are scheme-validated, task ids are Crockford base32 (no
// hyphen), and plan slugs carry a "cc-plan-" prefix.
func TestRequireFlagsFirst_RealPositionalsAreNotFlagLike(t *testing.T) {
	for _, pos := range []string{
		"https://example.com/a-b?c=d",
		"01KXKE2XZNQEA9R0EQEXMGP9C9",
		"cc-plan-global-search",
	} {
		_, err := parseWith(t, []string{pos})
		require.NoError(t, err, pos)
	}
}
