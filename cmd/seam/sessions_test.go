package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// `seam sessions --status bogus` reached the console's `default: filter = ""` and
// silently listed EVERY session. The client now rejects it at parse time; B5 also
// closed the server side, so a direct URL cannot do it either.
func TestSessionsParse_StatusEnum(t *testing.T) {
	_, err := parse(commands(), []string{"sessions", "--status", "bogus"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "valid values are active, completed, expired")

	for _, st := range core.SessionStatuses {
		p, perr := parse(commands(), []string{"sessions", "--status", string(st)})
		require.NoError(t, perr, "%s is canonical", st)
		require.Equal(t, string(st), *p.opts.(*sessionsOpts).status)
	}
}

// The derivation is load-bearing rather than stylistic. seam's --status help said
// "active|completed" while the console has always accepted "expired" -- so a list
// transcribed from the help text would have made a working filter a parse error.
// This is the case the plan flagged: copy the CODE, not the HELP TEXT.
func TestSessionsParse_ExpiredIsAcceptedDespiteTheOldHelpText(t *testing.T) {
	p, err := parse(commands(), []string{"sessions", "--status", "expired"})
	require.NoError(t, err, "the old help text omitted expired; the canonical set does not")
	require.Equal(t, "expired", *p.opts.(*sessionsOpts).status)
}

// The list and the detail view are one command: 0 or 1 positional.
func TestSessionsParse_Arity(t *testing.T) {
	p, err := parse(commands(), []string{"sessions"})
	require.NoError(t, err)
	require.Empty(t, p.pos)

	p, err = parse(commands(), []string{"sessions", "01K7ABCD"})
	require.NoError(t, err)
	require.Equal(t, []string{"01K7ABCD"}, p.pos)

	_, err = parse(commands(), []string{"sessions", "01AAA", "01BBB"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "takes at most 1 positional argument")
}

// sessions was the last command requireFlagsFirst guarded, and the one the docs
// used to illustrate the rule. Both orders now work.
func TestSessionsParse_FlagsAndIdInEitherOrder(t *testing.T) {
	for _, argv := range [][]string{
		{"sessions", "--status", "active", "01K7ABCD"},
		{"sessions", "01K7ABCD", "--status", "active"},
	} {
		p, err := parse(commands(), argv)
		require.NoError(t, err, "%v", argv)
		require.Equal(t, []string{"01K7ABCD"}, p.pos)
		require.Equal(t, "active", *p.opts.(*sessionsOpts).status)
	}
}

// usage and doctor take neither flags nor positionals, and used to accept and
// ignore anything handed to them.
func TestUsageAndDoctorParse_TakeNoArguments(t *testing.T) {
	for _, argv := range [][]string{
		{"usage", "extra"},
		{"doctor", "extra"},
	} {
		_, err := parse(commands(), argv)
		require.Error(t, err, "%v", argv)
		require.Contains(t, err.Error(), "takes no positional arguments")
	}
}
