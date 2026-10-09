package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/arctop/seamless/internal/config"
	"github.com/arctop/seamless/internal/hooks"
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

// The default (no --client) sends no client param, so the daemon still resolves
// the request to Claude Code exactly as it did before the discriminator existed.
// The query string is no longer empty -- the machine identity always rides on it
// (see identityParams) -- but the client half is absent, which is the half that
// selects a payload adapter.
func TestRunHook_NoClientFlagOmitsQueryParam(t *testing.T) {
	e, got := captureHookServer(t, `{"session_id":"abc","cwd":"/w"}`)
	require.NoError(t, runHook(context.Background(), e, &hookOpts{}, []string{"session-start"}))
	require.NotNil(t, *got, "seam hook must forward to the daemon")
	require.Equal(t, "/api/hooks/session-start", (*got).URL.Path)
	require.False(t, (*got).URL.Query().Has("client"),
		"no --client => no client param, so the daemon defaults to Claude Code")
}

// anyEventPayload is a hook payload every event forwards: post-tool-use's local
// filter passes an ExitPlanMode call, and the other events forward any body.
// Its cwd is outside any repository, so session-start's git reads find nothing.
const anyEventPayload = `{"session_id":"s","cwd":"/w","prompt":"hi","tool_name":"ExitPlanMode"}`

// hookEnvAt is a stub env whose config points seam hook at addr, with
// anyEventPayload on stdin.
func hookEnvAt(addr string) (*env, *bytes.Buffer, *bytes.Buffer) {
	e, out, errb := stubEnv()
	e.stdin = strings.NewReader(anyEventPayload)
	e.loadConfig = func() (config.Config, error) {
		cfg := config.Defaults()
		cfg.Addr = addr
		cfg.MCP.APIKey = "k"
		return cfg, nil
	}
	return e, out, errb
}

// answerHooks answers any hook with a fixed reply, or hangs up after reading it
// once hangUpAfterRead is set, counting what it read.
func answerHooks(hits *atomic.Int32, hangUpAfterRead bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		hits.Add(1)
		if hangUpAfterRead {
			hangUp(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"continue":true}`))
	})
}

// A hook that fires while an update restarts the daemon lands when the daemon
// is back within its budget: refused while the port is closed, delivered once
// the new daemon listens, and read by it exactly once. A full restart (about 2s
// on macOS) outlasts the short budget, so a hook on it fails open instead: the
// accepted cost is one prompt's recall injection or one heartbeat. The client
// matters where its hook does different work: Claude Code's subagent-stop
// caches a planning subagent's report and rides the restart out, Codex's only
// heartbeats and does not.
func TestRunHook_RidesOutARestartWithinItsBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		event  string
		client string // --client; "" is Claude Code
		// backAt is the retry wait during which the daemon listens again. The
		// production backoff ends wait 2 at 300ms (a blip) and wait 5 at 2.5s
		// (the next attempt after a 2s restart).
		backAt int
		waits  int
		lands  bool
	}{
		{"blip, full budget", "session-start", "", 2, 2, true},
		{"blip, short budget", "user-prompt-submit", "", 2, 2, true},
		{"restart, full budget", "session-start", "", 5, 5, true},
		{"restart, short budget", "user-prompt-submit", "", 5, 4, false},
		{"restart, claude code subagent-stop", "subagent-stop", "", 5, 5, true},
		{"restart, codex subagent-stop", "subagent-stop", "codex", 5, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := deadAddr(t)
			var hits atomic.Int32
			clk := &fakeClock{onWait: func(n int) {
				if n == tc.backAt {
					serveAt(t, addr, answerHooks(&hits, false))
				}
			}}
			e, out, errb := hookEnvAt(addr)
			o := &hookOpts{client: tc.client, retry: clk.policy}
			require.NoError(t, runHook(context.Background(), e, o, []string{tc.event}))
			require.Len(t, clk.recorded(), tc.waits)
			if !tc.lands {
				require.Empty(t, out.String())
				require.Contains(t, errb.String(), "seam hook: request not sent after 5 attempts over 1s")
				require.Zero(t, hits.Load())
				return
			}
			require.Equal(t, `{"continue":true}`, out.String())
			require.Empty(t, errb.String())
			require.Equal(t, int32(1), hits.Load())
		})
	}
}

// A daemon that is down for good costs each hook exactly its budget for that
// event and client on the production backoff, and then the hook fails open --
// the reason on stderr, nothing on stdout, exit 0. The full budget is nine
// refused dials over 5s, the short one five over 1s. Every event runs under
// every --client value, an absent one included.
func TestRunHook_DialBudgetFollowsTheEventAndClient(t *testing.T) {
	ms := time.Millisecond
	spend := map[time.Duration]struct {
		waits []time.Duration
		msg   string
	}{
		hookFullDialBudget: {
			[]time.Duration{100 * ms, 200 * ms, 400 * ms, 800 * ms, time.Second, time.Second, time.Second, 500 * ms},
			"seam hook: request not sent after 9 attempts over 5s",
		},
		hookShortDialBudget: {
			[]time.Duration{100 * ms, 200 * ms, 400 * ms, 300 * ms},
			"seam hook: request not sent after 5 attempts over 1s",
		},
	}
	for _, client := range append([]string{""}, hookClients...) {
		for _, h := range hookEvents {
			name := client
			if name == "" {
				name = "no client"
			}
			t.Run(name+"/"+h.event, func(t *testing.T) {
				budget := h.dialBudgetFor(client)
				want, ok := spend[budget]
				require.True(t, ok, "no expectation for a %s budget", budget)
				clk := &fakeClock{}
				e, out, errb := hookEnvAt(deadAddr(t))
				o := &hookOpts{client: client, retry: clk.policy}
				require.NoError(t, runHook(context.Background(), e, o, []string{h.event}))
				require.Empty(t, out.String())
				require.Contains(t, errb.String(), want.msg)
				require.Equal(t, want.waits, clk.recorded())
				require.Equal(t, budget, clk.waited())
			})
		}
	}
}

// A payload the daemon read before the connection dropped is never sent again:
// the hook fails open on the first failure, with nothing retried.
func TestRunHook_NeverResendsAfterSending(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(answerHooks(&hits, true))
	t.Cleanup(srv.Close)
	clk := &fakeClock{}
	e, out, errb := hookEnvAt(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, runHook(context.Background(), e, &hookOpts{retry: clk.policy}, []string{"user-prompt-submit"}))
	require.Empty(t, out.String())
	require.Contains(t, errb.String(), "seam hook: ")
	require.NotContains(t, errb.String(), errNotSent.Error(), "the payload was sent")
	require.Equal(t, int32(1), hits.Load())
	require.Empty(t, clk.recorded())
}

// The owner's split (2026-10-09): the full budget spans an update's restart for
// the hooks that are costly to lose, the short one only a blip for those whose
// loss is one prompt's recall or one heartbeat. subagent-stop is split by
// client: Claude Code's caches a planning subagent's report, Codex's only
// heartbeats. Pinned whole, every event under every client, so no event or
// client joins without a decision about its budget; each also leaves half of
// hookTimeout for an attempt that connects at the end of its budget.
func TestHookDialBudgets_ByClientAndEvent(t *testing.T) {
	const full, short = 5 * time.Second, time.Second
	want := map[string]map[string]time.Duration{
		"claude-code": {
			"session-start":      full,
			"user-prompt-submit": short,
			"session-end":        full,
			"post-tool-use":      full,
			"subagent-start":     full,
			"subagent-stop":      full,
			"permission-request": full,
			"stop":               short,
		},
		"codex": {
			"session-start":      full,
			"user-prompt-submit": short,
			"session-end":        full,
			"post-tool-use":      full,
			"subagent-start":     full,
			"subagent-stop":      short,
			"permission-request": full,
			"stop":               short,
		},
	}
	got := map[string]map[string]time.Duration{}
	for _, client := range hookClients {
		got[client] = map[string]time.Duration{}
		for _, h := range hookEvents {
			got[client][h.event] = h.dialBudgetFor(client)
			require.LessOrEqual(t, got[client][h.event], hookTimeout/2,
				"%s/%s: an attempt that connects at the end of the budget must still have time to be served", client, h.event)
		}
	}
	require.Equal(t, want, got)

	// An absent --client is Claude Code, as it is to the daemon.
	for _, h := range hookEvents {
		require.Equal(t, h.dialBudgetFor(hookClientClaudeCode), h.dialBudgetFor(""), "%s", h.event)
	}
}

// Every override is live: it names an event in hookEvents and a client in
// hookClients, once, and gives that client a budget other than the event's
// own. dialBudgetFor skips a misspelled event or client in silence, and an
// override equal to the default says the clients differ when they do not.
func TestHookClientDialBudgets_AreLiveOverrides(t *testing.T) {
	seen := map[[2]string]bool{}
	for _, o := range hookClientDialBudgets {
		h, ok := lookupHookEvent(o.event)
		require.True(t, ok, "an override for unknown event %q", o.event)
		require.Contains(t, hookClients, o.client, "an override for unknown client %q", o.client)
		require.NotEqual(t, h.dialBudget, o.budget, "%s/%s overrides the event's budget with itself", o.event, o.client)
		key := [2]string{o.event, o.client}
		require.False(t, seen[key], "%s/%s is overridden twice", o.event, o.client)
		seen[key] = true
	}
}

// Every Claude Code hook is installed without --client, so an absent one must
// take a Claude Code override rather than fall through to the event's own
// budget. No such override exists today, so this plants one.
func TestHookEvent_AbsentClientTakesClaudeCodeOverrides(t *testing.T) {
	saved := hookClientDialBudgets
	t.Cleanup(func() { hookClientDialBudgets = saved })
	hookClientDialBudgets = append(slices.Clone(saved),
		hookClientDialBudget{"session-start", hookClientClaudeCode, hookShortDialBudget})

	h, ok := lookupHookEvent("session-start")
	require.True(t, ok)
	require.Equal(t, hookShortDialBudget, h.dialBudgetFor(""))
	require.Equal(t, hookShortDialBudget, h.dialBudgetFor(hookClientClaudeCode))
	require.Equal(t, hookFullDialBudget, h.dialBudgetFor(hookClientCodex))
}

// Every budget fits inside the timeout its client gives that hook with half to
// spare, for every client and every command hook its profile wires, on either
// transport. Past half, a daemon that is down for good runs a hook into its
// client's kill: a timeout warning in place of a quiet fail-open, on every
// prompt in user-prompt-submit's case. A test-only import, like the event pin
// below.
func TestHookDialBudgets_FitInsideEveryClientTimeout(t *testing.T) {
	for _, client := range hooks.HookClients {
		timeouts, err := hooks.CommandHookTimeouts(client)
		require.NoError(t, err)
		require.NotEmpty(t, timeouts, "%s", client)
		for arg, timeout := range timeouts {
			h, ok := lookupHookEvent(arg)
			require.True(t, ok, "%s wires `seam hook %s`, which this CLI rejects", client, arg)
			budget := h.dialBudgetFor(string(client))
			require.LessOrEqual(t, budget, timeout/2,
				"%s kills `seam hook %s` at %s, so its %s budget must be at most half that", client, arg, timeout, budget)
		}
	}
	// An https install makes Claude Code's user-prompt-submit a command hook too
	// (profileForBaseURL), with the same 5s timeout Codex gives it.
	cc, err := hooks.CommandHookTimeouts(hooks.ClientClaudeCode)
	require.NoError(t, err)
	require.Equal(t, 5*time.Second, cc["user-prompt-submit"])
}

// The help names each budget from the table, never by hand, and marks the
// event whose budget depends on the client with the clients that spend each.
func TestHookCmd_HelpDerivesTheDialBudgets(t *testing.T) {
	require.Contains(t, commandHelp(hookCmd), "\ndial retry: 5s for session-start, session-end, post-tool-use, "+
		"subagent-start, subagent-stop (claude-code), permission-request; "+
		"1s for user-prompt-submit, subagent-stop (codex), stop\n")
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
		h, ok := lookupHookEvent(arg)
		require.True(t, ok, "install-hooks writes `seam hook %s`, which this CLI rejects", arg)
		require.Equal(t, endpoint, h.endpoint, "seam hook %s forwards somewhere the installer does not expect", arg)
	}
}

// Like the event table pin above, this keeps the thin CLI's enum identical to
// the daemon/install package's canonical set without importing that dependency
// graph into the production seam binary.
func TestHookClients_MatchTheServerCanonicalSet(t *testing.T) {
	require.Equal(t, enumOf(hooks.HookClients), hookClients)
}
