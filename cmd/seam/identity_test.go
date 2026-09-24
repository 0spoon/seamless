package main

// The machine identity this CLI resolves and sends: which host the hook fired
// on, and which repository its cwd belongs to. The daemon cannot work either out
// for a machine that is not its own, so if these stop being sent, a remote
// device's sessions quietly stop landing in the right project.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/hooks"
	"github.com/0spoon/seamless/internal/mcp"
)

// mkRepo makes a git checkout with an origin remote and returns its root.
func mkRepo(t *testing.T, name, origin string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	gitDir := filepath.Join(root, ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0o755))
	if origin != "" {
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "config"),
			[]byte("[remote \"origin\"]\n\turl = "+origin+"\n"), 0o644))
	}
	return root
}

// The pin that keeps the CLI's copy of the identity keys honest against the
// daemon's. Because a hook fails open, a mismatch is a silent no-op: the params
// are appended under names the daemon does not read, and a remote agent simply
// stops being placed.
func TestHookIdentityParams_MatchTheServerCanonicalSet(t *testing.T) {
	require.Equal(t, hooks.IdentityQueryParams(), hookIdentityParams)
}

// Same pin for the MCP side: the thin CLI keeps its own literal rather than
// importing the daemon's package, so the two must be compared somewhere.
func TestHostHeader_MatchesTheServerConstant(t *testing.T) {
	require.Equal(t, mcp.HostHeader, hostHeader)
}

// session-start carries the full identity, resolved on this machine.
func TestRunHook_SessionStartAppendsTheRepoIdentity(t *testing.T) {
	root := mkRepo(t, "myapp", "https://github.com/acme/myapp.git")
	sub := filepath.Join(root, "internal")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	e, got := captureHookServer(t, `{"session_id":"abc","cwd":`+quote(sub)+`}`)
	require.NoError(t, runHook(context.Background(), e, &hookOpts{}, []string{"session-start"}))
	require.NotNil(t, *got)

	q := (*got).URL.Query()
	require.Equal(t, config.Hostname(), q.Get("host"))
	require.Equal(t, root, q.Get("repo_root"))
	require.Equal(t, root, q.Get("main_root"))
	require.Equal(t, "https://github.com/acme/myapp.git", q.Get("origin"))
}

// A cwd outside any repository has nothing to place: the host still travels (it
// is what decides whether any later capture may read this disk), the roots do
// not.
func TestRunHook_OmitsTheRootsWhenNotInARepo(t *testing.T) {
	e, got := captureHookServer(t, `{"session_id":"abc","cwd":`+quote(t.TempDir())+`}`)
	require.NoError(t, runHook(context.Background(), e, &hookOpts{}, []string{"session-start"}))
	require.NotNil(t, *got)

	q := (*got).URL.Query()
	require.Equal(t, config.Hostname(), q.Get("host"))
	require.False(t, q.Has("repo_root"))
	require.False(t, q.Has("main_root"))
	require.False(t, q.Has("origin"))
}

// Every other hook sends the host and nothing else: the roots exist for
// placement, which only session-start does, and these hooks fire every turn.
func TestRunHook_NonSessionStartSendsOnlyTheHost(t *testing.T) {
	root := mkRepo(t, "myapp", "https://github.com/acme/myapp.git")
	e, got := captureHookServer(t, `{"session_id":"abc","cwd":`+quote(root)+`,"user_prompt":"hi"}`)
	require.NoError(t, runHook(context.Background(), e, &hookOpts{}, []string{"user-prompt-submit"}))
	require.NotNil(t, *got)

	q := (*got).URL.Query()
	require.Equal(t, config.Hostname(), q.Get("host"))
	require.False(t, q.Has("repo_root"), "only session-start places a cwd")
}

// The repo identity a linked worktree reports is its MAIN checkout's, because
// that is what project identity keys on.
func TestIdentityParams_WorktreeReportsBothRoots(t *testing.T) {
	main := mkRepo(t, "myapp", "https://github.com/acme/myapp.git")
	wt := filepath.Join(t.TempDir(), "myapp-feature")
	require.NoError(t, os.MkdirAll(wt, 0o755))
	adminDir := filepath.Join(main, ".git", "worktrees", "myapp-feature")
	require.NoError(t, os.MkdirAll(adminDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"),
		[]byte("gitdir: "+adminDir+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(adminDir, "commondir"),
		[]byte("../..\n"), 0o644))

	got := identityParams("session-start", []byte(`{"cwd":`+quote(wt)+`}`))
	require.Equal(t, wt, got["repo_root"])
	require.Equal(t, main, got["main_root"], "project identity keys on the main checkout")
	require.Equal(t, "https://github.com/acme/myapp.git", got["origin"])
}

// mcp-headers is what Claude Code runs at connect time, so the host has to be in
// its output or an http-registered MCP client is attributed to the daemon's box.
func TestRunMCPHeaders_IncludesTheHostHeader(t *testing.T) {
	e, out, _ := stubEnv()
	e.loadConfig = func() (config.Config, error) {
		cfg := config.Defaults()
		cfg.MCP.APIKey = "k"
		return cfg, nil
	}
	require.NoError(t, runMCPHeaders(context.Background(), e, &mcpHeadersOpts{}, nil))

	var headers map[string]string
	require.NoError(t, json.Unmarshal(out.Bytes(), &headers))
	require.Equal(t, "Bearer k", headers["Authorization"])
	require.Equal(t, config.Hostname(), headers[hostHeader])
}

// quote renders a path as a JSON string literal, so a Windows-shaped or
// space-carrying temp dir cannot break the fixture body.
func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
