package main

// seam mcp-proxy -- a transport-thin stdio<->streamable-HTTP bridge, so an MCP
// client that can only speak stdio reaches the same daemon /api/mcp endpoint the
// HTTP-native clients (Claude Code, the seam CLI) already use. Codex CLI is the
// first such client: `codex mcp add seamless -- <abs seam> mcp-proxy --config ...`.
//
// Why a bridge rather than pointing Codex straight at the HTTP endpoint (design
// decision D6):
//   - no secret duplicated into ~/.codex/config.toml;
//   - we never hand-edit or serialize another tool's live TOML -- registration
//     goes through `codex mcp add`, symmetric with the CC installer shelling to
//     `claude mcp add`;
//   - `bearer_token_env_var` needs the key exported in whatever shell launches
//     codex, which is fragile (upstream codex issue #30125);
//   - the bridge makes ANY stdio-only MCP client a supported Seamless client.
//
// It is deliberately dumb: it knows the MCP wire framing (newline-delimited
// JSON-RPC on stdio, streamable HTTP on the wire) and nothing about tools,
// arguments, or results. The one stateful thing it carries is the Mcp-Session-Id
// the daemon mints on initialize -- resending it on every later POST is what keeps
// the connection binding alive, so session_start inheritance works across calls.

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// headerSessionID is the streamable-HTTP session header (mcp-go's
// server.HeaderKeySessionID). The daemon mints it on initialize and requires it
// on every subsequent request; the bridge captures and replays it.
const headerSessionID = "Mcp-Session-Id"

// mcpProxyOpts carries the flags for `seam mcp-proxy`.
type mcpProxyOpts struct {
	config string // --config: abs seamless.yaml the installer bakes into the codex registration
}

// bindMCPProxy registers --config, the same escape from cwd-relative config
// search that `seam hook` uses: `codex mcp add` records an argv with no
// environment, so the path is a flag rather than the SEAMLESS_CONFIG env prefix.
func bindMCPProxy(fs *flag.FlagSet) *mcpProxyOpts {
	o := &mcpProxyOpts{}
	fs.StringVar(&o.config, "config", "", "path to seamless.yaml, so the proxy resolves config from any cwd")
	return o
}

var mcpProxyCmd = spec("mcp-proxy", groupBridge, "bridge a stdio MCP client to seamlessd over HTTP",
	noArgs(), bindMCPProxy, runMCPProxy).
	withLong(`An MCP client spawns this; it is not run by hand. It reads newline-delimited
JSON-RPC from stdin, forwards each message to the daemon's /api/mcp streamable
HTTP endpoint with the bearer key from config, and relays the reply back to
stdout, preserving Mcp-Session-Id so a session started over the bridge stays
bound across calls.

Register it with a stdio-only client, e.g. Codex CLI:

  codex mcp add seamless -- <abs seam> mcp-proxy --config <abs seamless.yaml>

If seamlessd is unreachable the bridge exits nonzero with the reason on stderr,
so the client surfaces a failed server rather than hanging.`)

func runMCPProxy(ctx context.Context, e *env, o *mcpProxyOpts, _ []string) error {
	// --config is config.Load's documented $SEAMLESS_CONFIG override, moved out of
	// the shell because `codex mcp add` records an argv with no environment. Same
	// path as runHook; setting it in this process is safe and keeps loadConfig's
	// search order the single code path.
	if o.config != "" {
		if err := os.Setenv("SEAMLESS_CONFIG", o.config); err != nil {
			return fmt.Errorf("set config path: %w", err)
		}
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	return newBridge(mcpBase(cfg)+"/api/mcp", cfg.MCP.APIKey).run(ctx, e.stdin, e.stdout)
}

// bridge forwards stdio MCP frames to a streamable-HTTP endpoint and back. It is
// single-goroutine by construction: each frame is read, forwarded, and its reply
// relayed before the next is read, which preserves request/response ordering
// without any per-request bookkeeping.
type bridge struct {
	endpoint  string
	apiKey    string
	client    *http.Client
	sessionID string // Mcp-Session-Id from initialize, replayed on later POSTs
}

func newBridge(endpoint, apiKey string) *bridge {
	return &bridge{
		endpoint: endpoint,
		apiKey:   apiKey,
		// A dial timeout so a down daemon fails fast, but no response timeout: a
		// tool call may be LLM-backed (recall embeddings, gardener_request) and
		// legitimately slow, and codex has its own tool_timeout_sec for that.
		client: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			},
		},
	}
}

// run reads newline-delimited JSON-RPC from r and relays each frame's reply to w
// until stdin closes (EOF -> nil). A transport failure reaching the daemon is
// fatal and returned, so the CLI exits nonzero with the reason on stderr.
func (b *bridge) run(ctx context.Context, r io.Reader, w io.Writer) error {
	// ReadBytes rather than a Scanner: a tool-call frame (e.g. memory_write with a
	// long body) can exceed a Scanner's default token cap, and ReadBytes has none.
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if frame := bytes.TrimSpace(line); len(frame) > 0 {
			if ferr := b.forward(ctx, frame, w); ferr != nil {
				return ferr
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read stdin: %w", err)
		}
	}
}

// forward POSTs one JSON-RPC frame to the daemon and relays the reply to w.
func (b *bridge) forward(ctx context.Context, frame []byte, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(frame))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+b.apiKey)
	if b.sessionID != "" {
		req.Header.Set(headerSessionID, b.sessionID)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		// A transport failure means the daemon is unreachable: the bridge cannot
		// serve, so this is fatal rather than a per-message hiccup.
		return fmt.Errorf("cannot reach seamlessd at %s (is the daemon running?): %w", b.endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Capture the session id the daemon mints on initialize; replaying it on later
	// POSTs is what keeps the connection binding (session_start inheritance) alive.
	if id := resp.Header.Get(headerSessionID); id != "" {
		b.sessionID = id
	}

	switch {
	case resp.StatusCode == http.StatusAccepted:
		// A notification (or a response with no reply): 202 with no body. Nothing
		// to relay -- writing an empty line would corrupt the stdio framing.
		return nil
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		// The streamable-HTTP server answers all normal JSON-RPC traffic with 200,
		// carrying JSON-RPC-level errors in the body. A non-2xx is therefore a
		// transport/protocol fault (bad content type, invalid/terminated session,
		// server error) the bridge cannot recover from per-message: fail so the
		// client restarts and re-initializes rather than hangs.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096)) //nolint:errcheck // best-effort read of an error body for the message; a partial read still helps
		return fmt.Errorf("seamlessd returned %s: %s", resp.Status, bytes.TrimSpace(body))
	}

	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")) //nolint:errcheck // an absent or unparseable content type just falls through to the JSON path
	if mediaType == "text/event-stream" {
		return relaySSE(resp.Body, w)
	}
	// application/json: a single JSON-RPC reply object.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	return writeFrame(w, body)
}

// writeFrame relays one JSON-RPC message as a single stdio frame: trimmed to one
// line, newline-terminated, per the stdio transport contract (no embedded
// newlines). An empty message writes nothing.
func writeFrame(w io.Writer, msg []byte) error {
	msg = bytes.TrimSpace(msg)
	if len(msg) == 0 {
		return nil
	}
	if _, err := w.Write(append(msg, '\n')); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	return nil
}

// relaySSE relays the JSON-RPC messages carried in a text/event-stream reply.
// The synchronous Seamless tools never upgrade to SSE, so this is defensive: the
// streamable-HTTP server can still choose it, and each event's data payload is
// one JSON-RPC message.
func relaySSE(body io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var data []string
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		msg := strings.Join(data, "\n")
		data = data[:0]
		return writeFrame(w, []byte(msg))
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "": // event boundary
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[len("data:"):], " "))
		default: // event:, id:, retry:, comments -- not part of the payload
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read sse: %w", err)
	}
	return flush()
}
