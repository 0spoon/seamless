package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
)

// healthzOnly builds an env pointed at a server that answers /healthz with body
// and nothing else, with dial failing: the shape of a seamlessd whose HTTP
// surface is up while MCP is unreachable or the static key is wrong.
//
// The green path is deliberately not stubbed here. e.dial hands back a concrete
// *mcpclient.Client, so faking a WORKING one means standing up a real MCP server,
// which no test in this package does -- cmd/seam tests parse and pure helpers, and
// the tool surface is tested in internal/mcp. statusErr is unit-tested below
// instead, which is where the exit-0 decision actually lives.
func healthzOnly(t *testing.T, body string) (*env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	e, out, errb := stubEnv()
	e.loadConfig = func() (config.Config, error) {
		cfg := config.Defaults()
		cfg.Addr = strings.TrimPrefix(srv.URL, "http://")
		cfg.DataDir = "/tmp/seamless-test"
		return cfg, nil
	}
	e.dial = func(context.Context) (*mcpclient.Client, config.Config, error) {
		return nil, config.Config{}, errors.New("connection refused")
	}
	return e, out, errb
}

// The bug this fixes: `seam status` printed `projects: (mcp unavailable: ...)`
// and returned nil. The command whose entire purpose is answering "is this thing
// up?" exited 0 when the answer was no -- useless as a scripted health gate, and
// silent in exactly the way a health check can least afford.
//
// Both halves are asserted together because both matter and they are easy to
// mistake for being in tension: the partial status is still PRINTED (it is what
// the caller asked for, and it names which half broke), and the command still
// FAILS.
func TestStatus_PrintsPartialStatusAndStillFails(t *testing.T) {
	e, out, errb := healthzOnly(t, `{"status":"ok","version":"test"}`)

	require.Equal(t, 1, dispatch(context.Background(), e, []string{"status"}),
		"mcp down must exit non-zero: this is a health gate")

	got := out.String()
	require.Contains(t, got, "server:   ok", "the half that answered is still reported")
	require.Contains(t, got, "data dir: /tmp/seamless-test")
	require.Contains(t, got, "projects: (mcp unavailable: connection refused)",
		"and the half that did not is named, not swallowed")

	// The error is returned once and printed once, by the caller. The printed lines
	// are the product, not a log of the error, which is why this is not an AGENTS.md
	// "log or return, never both" violation -- and why the result lands on stderr
	// while the output stays clean for a pipe.
	require.Contains(t, errb.String(), "status: 1 check(s) failed")
	require.NotContains(t, got, "check(s) failed", "the result is not part of the output")
}

// An unreadable /healthz means something other than seamlessd answered on the
// port. It used to print the reason and carry on to exit 0.
func TestStatus_UnreadableHealthCounts(t *testing.T) {
	e, out, errb := healthzOnly(t, `not json`)

	require.Equal(t, 1, dispatch(context.Background(), e, []string{"status"}))
	require.Contains(t, out.String(), "server:   (unreadable health response")
	// Two failures, not one: the health decode AND the mcp dial. The count is the
	// point of the doctor-shaped aggregate -- it does not stop at the first.
	require.Contains(t, errb.String(), "status: 2 check(s) failed")
}

// An unreachable server is a hard error before any check runs. This branch was
// always correct -- it is the precedent the mcp branches were inconsistent with,
// inside the same function.
func TestStatus_UnreachableServerErrors(t *testing.T) {
	e, _, errb := stubEnv()
	e.loadConfig = func() (config.Config, error) {
		cfg := config.Defaults()
		cfg.Addr = "127.0.0.1:1" // nothing listens on port 1
		return cfg, nil
	}
	require.Equal(t, 1, dispatch(context.Background(), e, []string{"status"}))
	require.Contains(t, errb.String(), "server unreachable at http://127.0.0.1:1")
}

// The exit-0 decision itself: no failures means no error. This is what keeps the
// tests above from being satisfied by a status that always fails.
func TestStatusErr_ZeroFailuresIsSuccess(t *testing.T) {
	require.NoError(t, statusErr(0))
	require.EqualError(t, statusErr(1), "status: 1 check(s) failed")
	require.EqualError(t, statusErr(3), "status: 3 check(s) failed")
}

// `seam status` takes no arguments; it used to accept and ignore them.
func TestStatus_TakesNoArguments(t *testing.T) {
	_, err := parse(commands(), []string{"status", "extra"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "takes no positional arguments")
}
