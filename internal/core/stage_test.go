package core

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseStageHeader(t *testing.T) {
	cases := []struct {
		name, body, status, gate string
	}{
		{"basic", "Status: in_progress\nGate: human\n\nthe body", "in_progress", "human"},
		{"reversed", "Gate: ai\nStatus: blocked", "blocked", "ai"},
		{"list-prefixed", "- Status: open\n- Gate: human", "open", "human"},
		{"status only", "Status: open\n\ndetails", "open", ""},
		{"case insensitive", "STATUS: Done", "done", ""},
		{"trailing prose", "Status: blocked waiting on hardware", "blocked", ""},
		{"markdown heading", "## Status: done\n\nshipped", "done", ""},
		{"heading with prose", "## Status: P6 cutover COMPLETE (2026-07-10)", "p6", ""},
		{"none", "just some prose\nno header here", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, gate := ParseStageHeader(c.body)
			require.Equal(t, c.status, status)
			require.Equal(t, c.gate, gate)
		})
	}
}

func TestStageStatusLive(t *testing.T) {
	for _, live := range []string{"open", "in_progress", "blocked"} {
		require.True(t, StageStatusLive(live), live)
	}
	for _, dead := range []string{"", StageStatusDone, "p6", "complete"} {
		require.False(t, StageStatusLive(dead), "%q is not a live gate", dead)
	}
}
