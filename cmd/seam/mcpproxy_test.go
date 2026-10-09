package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"

	"github.com/arctop/seamless/internal/agentguide"
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
	b := newBridge(srv.URL+"/api/mcp", "testkey", nil)
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
	b := newBridge(srv.URL+"/api/mcp", "k", nil)
	require.NoError(t, b.run(context.Background(),
		strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"), &out))
	require.Empty(t, out.String())
}

// rpcReply is one relayed frame, decoded far enough to tell a result from an
// error and to read the id back exactly as the client wrote it.
type rpcReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeReply(t *testing.T, frame string) rpcReply {
	t.Helper()
	var r rpcReply
	require.NoError(t, json.Unmarshal([]byte(frame), &r), "frame is not JSON-RPC: %s", frame)
	return r
}

// scriptedStdin hands the bridge one frame per Read and runs before(i) first.
// The bridge reads frame i only after it has answered frame i-1, so before is
// where a test changes the world between two frames.
type scriptedStdin struct {
	frames []string
	before func(i int)
	i      int
}

func (s *scriptedStdin) Read(p []byte) (int, error) {
	if s.i >= len(s.frames) {
		return 0, io.EOF
	}
	if s.before != nil {
		s.before(s.i)
	}
	n := copy(p, s.frames[s.i]+"\n")
	s.i++
	return n, nil
}

// fakeDaemon answers every JSON-RPC request with an empty result and every
// notification with a 202, hangs up (after reading the whole request) on any
// frame that mentions "hang-up", and counts what it read: served by method,
// hang-ups apart.
type fakeDaemon struct {
	mu      sync.Mutex
	served  []string
	hangUps int
}

func (d *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if bytes.Contains(body, []byte("hang-up")) {
		d.mu.Lock()
		d.hangUps++
		d.mu.Unlock()
		hangUp(w)
		return
	}
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	d.mu.Lock()
	d.served = append(d.served, msg.Method)
	d.mu.Unlock()
	if len(msg.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, msg.ID)
}

func (d *fakeDaemon) counts() ([]string, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.served...), d.hangUps
}

// The bridge rides out the restart an automatic update performs. The next call
// is refused while the port is closed, re-dialed until the new daemon listens,
// and served there exactly once -- under the session id the OLD daemon minted,
// which a fresh transport accepts because it checks the id's shape, not its
// origin. The transport is mcp-go's, configured as the daemon configures it.
func TestBridge_RidesOutADaemonRestart(t *testing.T) {
	addr := deadAddr(t)
	var mu sync.Mutex
	var calls []string // the Mcp-Session-Id each served tools/call carried
	daemon := func() http.Handler {
		srv := mcpserver.NewMCPServer("t", "0", mcpserver.WithToolCapabilities(false))
		srv.AddTool(mcp.NewTool("echo"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("echoed"), nil
		})
		transport := mcpserver.NewStreamableHTTPServer(srv,
			mcpserver.WithStreamableHTTPProtocolVersions(mcp.LegacyProtocolVersions()...))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if bytes.Contains(body, []byte(`"tools/call"`)) {
				mu.Lock()
				calls = append(calls, r.Header.Get(headerSessionID))
				mu.Unlock()
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			transport.ServeHTTP(w, r)
		})
	}

	old := serveAt(t, addr, daemon())
	clk := &fakeClock{onWait: func(n int) {
		if n == 2 {
			serveAt(t, addr, daemon()) // the new daemon binds the old address
		}
	}}
	b := newBridge("http://"+addr+"/api/mcp", "k", nil)
	b.retry = clk.policy(proxyDialBudget)
	ctx := context.Background()

	var out bytes.Buffer
	require.NoError(t, b.forward(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+
		mcp.LATEST_LEGACY_PROTOCOL_VERSION+`","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`), &out))
	minted := b.sessionID
	require.NotEmpty(t, minted, "initialize mints a session id: %s", out.String())
	require.NoError(t, b.forward(ctx, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), &out))

	// The update's restart. The old daemon's FIN makes the transport drop its
	// pooled connection well under a millisecond after it lands (dialretry.go);
	// the test drops it by hand rather than race that, since nothing in it waits
	// seconds the way a real restart does.
	old.Close()
	b.client.CloseIdleConnections()

	out.Reset()
	require.NoError(t, b.forward(ctx, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{}}}`), &out))
	reply := decodeReply(t, out.String())
	require.Nil(t, reply.Error, out.String())
	require.Contains(t, string(reply.Result), "echoed")
	require.JSONEq(t, `2`, string(reply.ID))
	require.Len(t, clk.recorded(), 2, "refused twice while the port was closed")
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{minted}, calls, "served once, by the new daemon, under the old session id")
}

// A daemon that stays down past the budget gets the request answered in-band
// with a JSON-RPC error naming the cause, and the bridge goes on: it serves the
// next frame once the daemon is back. Exiting instead would take the tools away
// for the rest of the session, since a stdio client does not restart its server.
func TestBridge_AnswersADownDaemonInBandAndKeepsServing(t *testing.T) {
	addr := deadAddr(t)
	d := &fakeDaemon{}
	clk := &fakeClock{}
	b := newBridge("http://"+addr+"/api/mcp", "k", nil)
	b.retry = clk.policy(proxyDialBudget)
	in := &scriptedStdin{
		frames: []string{
			`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			`{"jsonrpc":"2.0","id":"two","method":"tools/list"}`,
		},
		before: func(i int) {
			if i == 1 {
				serveAt(t, addr, d)
			}
		},
	}

	var out bytes.Buffer
	require.NoError(t, b.run(context.Background(), in, &out))
	frames := nonEmptyLines(out.String())
	require.Len(t, frames, 2)

	failed := decodeReply(t, frames[0])
	require.JSONEq(t, `1`, string(failed.ID))
	require.NotNil(t, failed.Error, frames[0])
	require.Equal(t, mcp.INTERNAL_ERROR, failed.Error.Code)
	require.Contains(t, failed.Error.Message, "cannot reach seamlessd at http://"+addr+"/api/mcp")
	require.Contains(t, failed.Error.Message, "request not sent after 34 attempts over 30s")
	require.Equal(t, proxyDialBudget, clk.waited())

	served := decodeReply(t, frames[1])
	require.Nil(t, served.Error, frames[1])
	require.JSONEq(t, `"two"`, string(served.ID), "a string id comes back as the client wrote it")
	got, hangUps := d.counts()
	require.Equal(t, []string{"tools/list"}, got)
	require.Zero(t, hangUps)
}

// A request the daemon read before the connection dropped is never sent again --
// a tool call is not idempotent, and this one may already have run. It is
// answered in-band instead, and the bridge serves the next frame. A reused
// keep-alive connection is the case net/http has its own retry for.
func TestBridge_NeverResendsAfterSending(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames []string
	}{
		{"fresh connection", []string{
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hang-up"}}`,
			`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		}},
		{"reused keep-alive connection", []string{
			`{"jsonrpc":"2.0","id":0,"method":"ping"}`,
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hang-up"}}`,
			`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDaemon{}
			srv := httptest.NewServer(d)
			t.Cleanup(srv.Close)
			clk := &fakeClock{}
			b := newBridge(srv.URL+"/api/mcp", "k", nil)
			b.retry = clk.policy(proxyDialBudget)

			var out bytes.Buffer
			require.NoError(t, b.run(context.Background(), strings.NewReader(strings.Join(tc.frames, "\n")+"\n"), &out))
			frames := nonEmptyLines(out.String())
			require.Len(t, frames, len(tc.frames), "one reply per request")

			lost := decodeReply(t, frames[len(frames)-2])
			require.JSONEq(t, `1`, string(lost.ID))
			require.NotNil(t, lost.Error, frames[len(frames)-2])
			require.Equal(t, mcp.INTERNAL_ERROR, lost.Error.Code)
			require.Contains(t, lost.Error.Message, "after sending the request")
			require.Contains(t, lost.Error.Message, "may already have run")
			next := decodeReply(t, frames[len(frames)-1])
			require.JSONEq(t, `2`, string(next.ID))
			require.Nil(t, next.Error, frames[len(frames)-1])

			_, hangUps := d.counts()
			require.Equal(t, 1, hangUps, "the daemon must read the call once")
			require.Empty(t, clk.recorded(), "nothing was retried")
		})
	}
}

// A notification, or a response to a server-initiated request, has no reply for
// the bridge to give: its failure goes to stderr, nothing reaches stdout for it,
// and the bridge serves the next frame.
func TestBridge_UnanswerableFailuresGoToStderr(t *testing.T) {
	for _, tc := range []struct {
		name, frame, want string
	}{
		{"notification", `{"jsonrpc":"2.0","method":"notifications/hang-up"}`,
			"seam mcp-proxy: notifications/hang-up: lost the connection to seamlessd"},
		{"response to a server request", `{"jsonrpc":"2.0","id":"hang-up","result":{}}`,
			"seam mcp-proxy: lost the connection to seamlessd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDaemon{}
			srv := httptest.NewServer(d)
			t.Cleanup(srv.Close)
			b := newBridge(srv.URL+"/api/mcp", "k", nil)
			var errb bytes.Buffer
			b.stderr = &errb

			var out bytes.Buffer
			require.NoError(t, b.run(context.Background(),
				strings.NewReader(tc.frame+"\n"+`{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n"), &out))
			frames := nonEmptyLines(out.String())
			require.Len(t, frames, 1, "only the request is answered")
			require.JSONEq(t, `2`, string(decodeReply(t, frames[0]).ID))
			require.Contains(t, errb.String(), tc.want)
			_, hangUps := d.counts()
			require.Equal(t, 1, hangUps, "sent once, never again")
		})
	}
}

// Cancelling the bridge's context ends a retry wait at once, and the bridge
// returns rather than answering: it is shutting down.
func TestBridge_CancelEndsARetryWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newBridge("http://"+deadAddr(t)+"/api/mcp", "k", nil)
	b.retry.backoff = func(int) time.Duration { return time.Hour }
	waiting := make(chan struct{})
	var once sync.Once
	b.retry.wait = func(ctx context.Context, d time.Duration) error {
		once.Do(func() { close(waiting) })
		return waitCtx(ctx, d)
	}

	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- b.run(ctx, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"), &out)
	}()
	select {
	case <-waiting:
	case err := <-done:
		t.Fatalf("returned before waiting to retry: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the refused dial was never retried")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, out.String())
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not end the retry wait")
	}
}

// A non-2xx that is not a JSON-RPC error is a failed exchange like any other:
// answered in-band, and the bridge goes on. Exiting on it, as the bridge once
// did, would take the tools away over one oversized or refused call.
func TestBridge_Non2xxIsAnsweredInBand(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"server error", http.StatusInternalServerError, "boom", "seamlessd returned 500 Internal Server Error: boom"},
		{"a result is not an error", http.StatusBadRequest, `{"jsonrpc":"2.0","id":1,"result":{}}`, "seamlessd returned 400 Bad Request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				if hits.Add(1) == 1 {
					w.WriteHeader(tc.status)
					_, _ = fmt.Fprint(w, tc.body)
					return
				}
				_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":2,"result":{}}`)
			}))
			t.Cleanup(srv.Close)

			var out bytes.Buffer
			require.NoError(t, newBridge(srv.URL, "k", nil).run(context.Background(), strings.NewReader(
				`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"+`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n"), &out))
			frames := nonEmptyLines(out.String())
			require.Len(t, frames, 2)
			failed := decodeReply(t, frames[0])
			require.JSONEq(t, `1`, string(failed.ID))
			require.NotNil(t, failed.Error, frames[0])
			require.Contains(t, failed.Error.Message, tc.want)
			require.Nil(t, decodeReply(t, frames[1]).Error, frames[1])
		})
	}
}

// An SSE reply that ends early is answered in-band only if the reply itself did
// not get through: before it, the client is still waiting on the id; after it,
// a second answer would send the id twice. The reply can also arrive in a final
// event with no blank line after it, which only the end-of-stream flush relays.
func TestBridge_SSEStreamThatEndsEarly(t *testing.T) {
	note := "event: message\ndata: " + `{"jsonrpc":"2.0","method":"notifications/progress","params":{}}` + "\n\n"
	reply := "event: message\ndata: " + `{"jsonrpc":"2.0","id":7,"result":{}}` + "\n\n"
	for _, tc := range []struct {
		name   string
		stream string
		hangUp bool     // drop the connection after the stream, mid-response
		want   []string // the method, or result/error and id, of each relayed frame
	}{
		{"connection lost before the reply", note, true, []string{"notifications/progress", "error 7"}},
		{"connection lost after the reply", note + reply, true, []string{"notifications/progress", "result 7"}},
		{"stream closed with no reply", note, false, []string{"notifications/progress", "error 7"}},
		{"reply in an unterminated final event", note + strings.TrimSuffix(reply, "\n\n"), false,
			[]string{"notifications/progress", "result 7"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tc.stream)
				if tc.hangUp {
					_ = http.NewResponseController(w).Flush() // a failure shows up as missing frames
					hangUp(w)
				}
			}))
			t.Cleanup(srv.Close)

			var out bytes.Buffer
			require.NoError(t, newBridge(srv.URL, "k", nil).run(context.Background(),
				strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call"}`+"\n"), &out))
			var got []string
			for _, f := range nonEmptyLines(out.String()) {
				var m struct {
					Method string          `json:"method"`
					ID     json.RawMessage `json:"id"`
					Error  json.RawMessage `json:"error"`
				}
				require.NoError(t, json.Unmarshal([]byte(f), &m), f)
				switch {
				case m.Method != "":
					got = append(got, m.Method)
				case len(m.Error) > 0:
					got = append(got, "error "+string(m.ID))
				default:
					got = append(got, "result "+string(m.ID))
				}
			}
			require.Equal(t, tc.want, got, out.String())
		})
	}
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
	b := newBridge(srv.URL+"/api/mcp", "k", nil)
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

	b := newBridge(srv.URL+"/api/mcp", "k", nil)
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

// The bridge names the agent that spawned it on every request -- the identity
// that binds the connection to that agent's ambient session -- and sends no
// header at all when it could not resolve one, rather than an empty value.
func TestBridge_ForwardsTheAgentProcess(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get(agentProcessHeader)+"|"+strconv.FormatBool(len(r.Header.Values(agentProcessHeader)) > 0))
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	note := `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	b := newBridge(srv.URL+"/api/mcp", "testkey", nil)
	b.agentProcess = "4242.17"
	require.NoError(t, b.run(context.Background(), strings.NewReader(note), io.Discard))

	anon := newBridge(srv.URL+"/api/mcp", "testkey", nil)
	require.NoError(t, anon.run(context.Background(), strings.NewReader(note), io.Discard))

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"4242.17|true", "|false"}, seen)
}

// statelessToolCall is a tools/call frame on the stateless MCP revision: no
// handshake, the version declared in _meta. A stdio client writes exactly this,
// with no headers.
const statelessToolCall = `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo","arguments":{},` +
	`"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
	`"io.modelcontextprotocol/clientInfo":{"name":"t","version":"0"},"io.modelcontextprotocol/clientCapabilities":{}}}}`

// echoTransport is mcp-go's real streamable transport over one trivial tool, so
// the bridge is checked against the header validation the daemon actually runs.
func echoTransport(t *testing.T, opts ...mcpserver.StreamableHTTPOption) string {
	t.Helper()
	srv := mcpserver.NewMCPServer("t", "0", mcpserver.WithToolCapabilities(false))
	srv.AddTool(mcp.NewTool("echo"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("echoed"), nil
	})
	ts := httptest.NewServer(mcpserver.NewStreamableHTTPServer(srv, opts...))
	t.Cleanup(ts.Close)
	return ts.URL
}

// A stateless frame through the bridge must reach the version check with the
// headers that revision requires, and its refusal must come back as the reply:
// the daemon serves only the session revisions, and the refusal naming them is
// how the client learns to negotiate down. Without the headers it would be a
// header mismatch instead; relaying nothing (a fatal exit) would restart the
// client into the same refusal.
func TestBridge_StatelessRefusalIsRelayed(t *testing.T) {
	url := echoTransport(t, mcpserver.WithStreamableHTTPProtocolVersions(mcp.LegacyProtocolVersions()...))
	var out bytes.Buffer
	require.NoError(t, newBridge(url, "k", nil).run(context.Background(), strings.NewReader(statelessToolCall+"\n"), &out))

	var reply struct {
		ID    float64 `json:"id"`
		Error struct {
			Code int `json:"code"`
			Data struct {
				Supported []string `json:"supported"`
			} `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &reply), out.String())
	require.Equal(t, float64(7), reply.ID)
	require.Equal(t, mcp.UNSUPPORTED_PROTOCOL_VERSION, reply.Error.Code, out.String())
	require.Contains(t, reply.Error.Data.Supported, mcp.LATEST_LEGACY_PROTOCOL_VERSION)
}

// Served the stateless revision, the same frame is answered: the bridge supplies
// every routing header a tools/call needs (version, method and name).
func TestBridge_StatelessFrameIsServed(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, newBridge(echoTransport(t), "k", nil).run(context.Background(),
		strings.NewReader(statelessToolCall+"\n"), &out))
	require.Contains(t, out.String(), "echoed")
	require.Equal(t, float64(7), jsonRPCID(t, out.String()))
}
