package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/hooks"
)

// The whole reason hook is exempt from the usage exit code: Claude Code reads
// exit 2 from a hook as a BLOCKING error and feeds stderr back to the model, so
// a typo'd hook config would wedge the very session the hook exists to serve.
// Every way of getting the command line wrong must still fail open at 1.
//
// stubEnv's dial and loadConfig are nil, so any case reaching the network would
// panic rather than pass.
func TestDispatch_HookNeverExitsTwo(t *testing.T) {
	for _, tt := range []struct {
		name string
		argv []string
		want string
	}{
		{"no event", []string{"hook"}, "missing hook event"},
		{"unknown event", []string{"hook", "bogus"}, `unknown hook event "bogus"`},
		{"unknown flag", []string{"hook", "--bogus"}, "not defined"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e, _, errb := stubEnv()
			require.Equal(t, 1, dispatch(context.Background(), e, tt.argv),
				"hook must fail open at 1: exit 2 blocks the session it serves")
			require.Contains(t, errb.String(), tt.want)
		})
	}
}

// Both halves of the exemption, at the layer that decides each. usageExit covers
// a line parse rejects; hook's loose arity keeps parse from rejecting one at all.
func TestUsageExit_HookIsTheOnlyExemption(t *testing.T) {
	for _, c := range commands() {
		want := 2
		if c.name == "hook" {
			want = 1
		}
		require.Equal(t, want, c.usageExit(), "%s", c.name)
	}

	// hook declares no enum and no upper bound, so a bad event reaches runHook
	// (which fails open) rather than the parse layer (which cannot).
	p, err := parse(commands(), []string{"hook", "bogus"})
	require.NoError(t, err, "hook must not enforce its event name at parse time")
	require.Equal(t, []string{"bogus"}, p.pos)

	p, err = parse(commands(), []string{"hook"})
	require.NoError(t, err, "hook must not enforce arity at parse time")
	require.Empty(t, p.pos)
}

// The error names the valid set rather than the old "(want a|b|c)" blob, and
// derives it from hookEvents so it cannot drift from what forwards.
func TestRunHook_ErrorsNameTheValidEvents(t *testing.T) {
	e, _, _ := stubEnv()
	err := runHook(context.Background(), e, &noOpts{}, []string{"bogus"})
	require.ErrorContains(t, err, "valid values are session-start, user-prompt-submit")
	require.NotContains(t, err.Error(), "want ")
}

// The pin that keeps the CLI's copy of the event table honest against the
// installer's canonical one. Because a hook fails open, a mismatch here is a
// silent no-op: install-hooks writes `seam hook <arg>` lines, and an arg this CLI
// rejects (or forwards to the wrong route) shows up only as a briefing that
// stopped arriving. A test-only import, so the CLI binary stays thin.
func TestHookEvents_MatchTheInstaller(t *testing.T) {
	installed := hooks.CommandHookEndpoints()
	require.NotEmpty(t, installed)
	for arg, endpoint := range installed {
		ep, ok := hookEndpoint(arg)
		require.True(t, ok, "install-hooks writes `seam hook %s`, which this CLI rejects", arg)
		require.Equal(t, endpoint, ep, "seam hook %s forwards somewhere the installer does not expect", arg)
	}
}
