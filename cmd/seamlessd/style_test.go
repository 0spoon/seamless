package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests run with stdout not a terminal, so colorEnabled is false and the style
// wrappers are no-ops -- the assertions below compare plain text.

func TestTildePath(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	require.NotEmpty(t, home)

	require.Equal(t, "~", tildePath(home))
	require.Equal(t, "~/.claude/settings.json",
		tildePath(filepath.Join(home, ".claude", "settings.json")))
	// A path outside home is returned verbatim.
	require.Equal(t, "/etc/seamless.yaml", tildePath("/etc/seamless.yaml"))
	// A sibling that merely shares the home prefix is not under home.
	require.Equal(t, home+"-backup", tildePath(home+"-backup"))
}
