package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/agentguide"
)

// jsonRPCID pulls the id out of one relayed frame, so a test can assert the
// bridge relayed the right reply for the right request.
func jsonRPCID(t *testing.T, frame string) float64 {
	t.Helper()
	var m struct {
		ID float64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(frame), &m), "frame is not JSON-RPC: %s", frame)
	return m.ID
}

func nonEmptyLines(s string) []string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// The headline contract: initialize/tools-list/tools-call round-trip, the
// Mcp-Session-Id minted on initialize replayed on every later POST (so the
// daemon's connection binding -- session_start inheritance -- survives), and a
// notification relayed as silence rather than an empty frame.
func TestBridge_RoundTripPersistsSession(t *testing.T) {
	var mu sync.Mutex
	var seenSessions []string // Mcp-Session-Id header per request, in arrival order

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/mcp", r.URL.Path)
		require.Equal(t, "Bearer testkey", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.Contains(t, r.Header.Get("Accept"), "text/event-stream")

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		require.NoError(t, json.Unmarshal(body, &msg))

		mu.Lock()
		seenSessions = append(seenSessions, r.Header.Get(headerSessionID))
		mu.Unlock()

		// A notification carries no id: 202 with no body, exactly as the daemon's
		// streamable-HTTP server answers one.
		if len(msg.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if msg.Method == "initialize" {
			w.Header().Set(headerSessionID, "sess-123")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      msg.ID,
			"result": map[string]any{
				"method":       msg.Method,
				"instructions": agentguide.MCPInstructions,
			},
		}))
	}))
	defer srv.Close()

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"recall"}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	b := newBridge(srv.URL+"/api/mcp", "testkey")
	require.NoError(t, b.run(context.Background(), strings.NewReader(in), &out))

	// Three replies; the notification relayed as silence.
	frames := nonEmptyLines(out.String())
	require.Len(t, frames, 3)
	require.Equal(t, float64(1), jsonRPCID(t, frames[0]))
	require.Equal(t, float64(2), jsonRPCID(t, frames[1]))
	require.Equal(t, float64(3), jsonRPCID(t, frames[2]))
	var initialized struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(frames[0]), &initialized))
	require.Equal(t, agentguide.MCPInstructions, initialized.Result.Instructions)

	// No session id on the first POST; minted on initialize and replayed on all
	// three that follow.
	require.Equal(t, []string{"", "sess-123", "sess-123", "sess-123"}, seenSessions)
}

// A no-id frame gets a 202: the bridge must write nothing, not a blank frame that
// would desync the stdio parser downstream.
func TestBridge_NotificationRelaysNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	var out bytes.Buffer
	b := newBridge(srv.URL+"/api/mcp", "k")
	require.NoError(t, b.run(context.Background(),
		strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"), &out))
	require.Empty(t, out.String())
}

// The daemon-down contract: a transport failure is fatal and names the endpoint,
// so the CLI exits nonzero with the reason on stderr rather than the client
// hanging on a server that never answers. A started-then-closed server gives a
// deterministic connection refusal.
func TestBridge_DaemonDownIsFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := srv.URL + "/api/mcp"
	srv.Close()

	b := newBridge(endpoint, "k")
	err := b.run(context.Background(),
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n"), io.Discard)
	require.Error(t, err)
	require.ErrorContains(t, err, "cannot reach seamlessd")
}

// A non-2xx is a transport/protocol fault (the daemon answers real JSON-RPC,
// errors included, with 200), so the bridge fails rather than relaying a
// non-JSON-RPC body as if it were a reply.
func TestBridge_Non2xxIsFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := newBridge(srv.URL+"/api/mcp", "k")
	err := b.run(context.Background(),
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"), io.Discard)
	require.Error(t, err)
	require.ErrorContains(t, err, "seamlessd returned 500")
}

// The daemon can answer a request with an SSE stream instead of a JSON body; the
// bridge must relay each event's data payload as one stdio frame.
func TestBridge_RelaysSSEReplies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{}}\n\n")
	}))
	defer srv.Close()

	var out bytes.Buffer
	b := newBridge(srv.URL+"/api/mcp", "k")
	require.NoError(t, b.run(context.Background(),
		strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call"}`+"\n"), &out))

	frames := nonEmptyLines(out.String())
	require.Len(t, frames, 1)
	require.Equal(t, float64(7), jsonRPCID(t, frames[0]))
}

// A frame with no trailing newline before EOF (a client that closes the pipe
// without a final delimiter) must still be forwarded, not dropped.
func TestBridge_ForwardsUnterminatedFinalFrame(t *testing.T) {
	var got int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		got++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	b := newBridge(srv.URL+"/api/mcp", "k")
	require.NoError(t, b.run(context.Background(),
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`), io.Discard)) // no "\n"
	require.Equal(t, 1, got)
}

// mcp-proxy is machine-invoked but not a Claude Code hook, so it takes the normal
// usage exit (2), not hook's fail-open 1. It also parses --config and rejects
// stray positionals at parse time.
func TestMCPProxy_ParsesConfigAndTakesNoPositionals(t *testing.T) {
	p, err := parse(commands(), []string{"mcp-proxy", "--config", "/abs/seamless.yaml"})
	require.NoError(t, err)
	require.Equal(t, "/abs/seamless.yaml", p.opts.(*mcpProxyOpts).config)
	require.Empty(t, p.pos)

	_, err = parse(commands(), []string{"mcp-proxy", "extra"})
	require.ErrorContains(t, err, "takes no positional arguments")

	require.Equal(t, 2, mcpProxyCmd.usageExit())
}
