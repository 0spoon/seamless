package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
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
		{"invalid client", []string{"hook", "session-start", "--client", "codxe"}, `valid values are claude-code, codex`},
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
// parse failures; hook's loose arity keeps event validation in runHook while the
// canonical client enum rejects a bad discriminator during parse.
func TestUsageExit_HookIsTheOnlyExemption(t *testing.T) {
	for _, c := range commands() {
		want := 2
		if c.name == "hook" {
			want = 1
		}
		require.Equal(t, want, c.usageExit(), "%s", c.name)
	}

	// A bad event reaches runHook (which fails open) rather than the parse layer.
	p, err := parse(commands(), []string{"hook", "bogus"})
	require.NoError(t, err, "hook must not enforce its event name at parse time")
	require.Equal(t, []string{"bogus"}, p.pos)

	p, err = parse(commands(), []string{"hook"})
	require.NoError(t, err, "hook must not enforce arity at parse time")
	require.Empty(t, p.pos)

	_, err = parse(commands(), []string{"hook", "session-start", "--client", "codxe"})
	require.EqualError(t, err,
		`invalid value "codxe" for flag -client: valid values are claude-code, codex`)
}

// A typo is an install/configuration error, not a recoverable daemon outage. It
// fails before stdin, config, or network work, names the canonical set, and uses
// exit 1 because hook exit 2 is blocking in supported agent clients.
func TestDispatch_HookInvalidClientExitContract(t *testing.T) {
	e, _, errb := stubEnv()
	code := dispatch(context.Background(), e,
		[]string{"hook", "session-start", "--client", "codxe"})
	require.Equal(t, 1, code)
	lines := strings.Split(strings.TrimSpace(errb.String()), "\n")
	require.Equal(t,
		`error: invalid value "codxe" for flag -client: valid values are claude-code, codex`,
		lines[0])
	require.Len(t, lines, 2)
	require.Equal(t, "usage: "+synopsis(hookCmd), lines[1])
}

// The error names the valid set rather than the old "(want a|b|c)" blob, and
// derives it from hookEvents so it cannot drift from what forwards.
func TestRunHook_ErrorsNameTheValidEvents(t *testing.T) {
	e, _, _ := stubEnv()
	err := runHook(context.Background(), e, &hookOpts{}, []string{"bogus"})
	require.ErrorContains(t, err, "valid values are session-start, user-prompt-submit")
	require.NotContains(t, err.Error(), "want ")
}

// captureHookServer stands in for seamlessd: it records the path+query and
// bearer of the one request seam hook forwards, and returns an empty 200 so the
// hook succeeds. loadConfig points seam hook at it via cfg.Addr.
func captureHookServer(t *testing.T, payload string) (*env, **http.Request) {
	t.Helper()
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	e, _, _ := stubEnv()
	e.stdin = strings.NewReader(payload)
	e.loadConfig = func() (config.Config, error) {
		cfg := config.Defaults()
		cfg.Addr = strings.TrimPrefix(srv.URL, "http://")
		cfg.MCP.APIKey = "k"
		return cfg, nil
	}
	return e, &got // caller reads *got after runHook returns
}

// --client threads the discriminator to the daemon as ?client=<value> on the
// forwarded request, so the server can pick the right per-client payload adapter.
func TestRunHook_ClientFlagForwardsQueryParam(t *testing.T) {
	for _, client := range hookClients {
		t.Run(client, func(t *testing.T) {
			e, got := captureHookServer(t, `{"session_id":"019f7291-x","cwd":"/w","prompt":"hi"}`)
			require.NoError(t, runHook(context.Background(), e, &hookOpts{client: client}, []string{"user-prompt-submit"}))
			require.NotNil(t, *got, "seam hook must forward to the daemon")
			require.Equal(t, "/api/hooks/user-prompt-submit", (*got).URL.Path)
			require.Equal(t, client, (*got).URL.Query().Get("client"))
		})
	}
}

// The default (no --client) leaves the forwarded URL byte-identical to before --
// no query string at all -- so every existing Claude Code hook is untouched.
func TestRunHook_NoClientFlagOmitsQueryParam(t *testing.T) {
	e, got := captureHookServer(t, `{"session_id":"abc","cwd":"/w"}`)
	require.NoError(t, runHook(context.Background(), e, &hookOpts{}, []string{"session-start"}))
	require.NotNil(t, *got, "seam hook must forward to the daemon")
	require.Equal(t, "/api/hooks/session-start", (*got).URL.Path)
	require.Empty(t, (*got).URL.RawQuery, "no --client => no query string, CC request unchanged")
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

// Like the event table pin above, this keeps the thin CLI's enum identical to
// the daemon/install package's canonical set without importing that dependency
// graph into the production seam binary.
func TestHookClients_MatchTheServerCanonicalSet(t *testing.T) {
	require.Equal(t, enumOf(hooks.HookClients), hookClients)
}
