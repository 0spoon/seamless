package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Codex's hook payloads differ from Claude Code's in a few field names (captured
// live and versioned in internal/hooks/testdata/codex). A per-client adapter
// decodes each raw hook body into the single
// internal struct the handlers already use, so project registration, briefing
// assembly, ambient sessions, and recall stay shared code with no per-client
// branches downstream. Claude Code is the identity adapter -- its payloads already
// ARE the internal shape -- so only Codex's renames live here.
//
// The client discriminator arrives OUT OF BAND, as the ?client= query param that
// `seam hook --client` appends, not in the body: the body decode is itself
// client-specific (Codex names the submitted prompt differently), so the handler
// must know the client BEFORE it can decode. An absent value resolves to Claude
// Code, so every existing CC hook -- which sends no client and no query param --
// is byte-for-byte unchanged. A present unknown value is rejected before decode.

// clientQueryParam is the query key `seam hook --client` sets on the forwarded
// request. It is the sole transport for the discriminator -- the body never
// carries it. cmd/seam keeps its own copy of this literal (it must not import
// this package, which would drag SQLite into a binary whose job is one POST); the
// two are test-pinned so drift cannot silently drop the discriminator.
const clientQueryParam = "client"

// clientFromRequest reads the client discriminator from the request's ?client=
// query param. A missing key defaults to Claude Code; a present empty, duplicate,
// or unknown value is invalid.
func clientFromRequest(r *http.Request) (Client, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", fmt.Errorf("invalid hook client query: %w; valid values are %s", err, hookClientNames())
	}
	values, present := query[clientQueryParam]
	if !present {
		return parseClient("", false)
	}
	if len(values) != 1 {
		return "", fmt.Errorf("invalid hook client query: expected exactly one value; valid values are %s", hookClientNames())
	}
	return parseClient(values[0], true)
}

// requireRequestClient is the shared authenticated hook boundary. Every route
// calls it before reading the body or mutating state, including Claude-only
// routes that otherwise ignore the parsed client.
func requireRequestClient(w http.ResponseWriter, r *http.Request) (Client, bool) {
	client, err := clientFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", false
	}
	return client, true
}

// readHookBody reads a hook request body under the shared size cap, tolerating a
// read or over-size error as an empty body: the caller's adapter then yields the
// zero payload and the handler no-ops rather than blocking the agent (the same
// never-block contract as a decode error).
func readHookBody(w http.ResponseWriter, r *http.Request) []byte {
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}
	return body
}

// decodeSessionStart decodes a SessionStart body into hookPayload. Both clients
// name every field this hook reads identically (session_id, cwd, source,
// transcript_path -- verified against the Codex fixture), so the decode does not
// branch on client; the parameter is kept so the adapter surface is uniform and a
// future SessionStart divergence has one obvious home. Tolerant: a decode error
// leaves the zero payload.
func decodeSessionStart(_ Client, body []byte) hookPayload {
	var p hookPayload
	_ = json.Unmarshal(body, &p) //nolint:errcheck // tolerant: a decode error leaves p zero
	return p
}

// decodePrompt decodes a UserPromptSubmit body into promptPayload, normalizing
// Codex's `prompt` field to Claude Code's `user_prompt` -- the one rename this
// hook needs, per the fixtures. A CC body already carries `user_prompt`, so the
// Codex branch never runs for it. Tolerant: a decode error leaves the zero payload.
func decodePrompt(client Client, body []byte) promptPayload {
	var p promptPayload
	_ = json.Unmarshal(body, &p) //nolint:errcheck // tolerant: a decode error leaves p zero
	if client == ClientCodex && p.UserPrompt == "" {
		var cx struct {
			Prompt string `json:"prompt"`
		}
		if err := json.Unmarshal(body, &cx); err == nil {
			p.UserPrompt = cx.Prompt
		}
	}
	return p
}

// decodeSessionEnd decodes a SessionEnd body into endPayload. Codex through
// 0.144.6 fires no SessionEnd (session end is reaper-driven off Stop -- design
// decision D5), so this path is Claude Code-only in practice; the decode is
// client-agnostic and the parameter is kept for the uniform adapter surface.
// Tolerant: a decode error leaves the zero payload (no session id -> no-op).
func decodeSessionEnd(_ Client, body []byte) endPayload {
	var p endPayload
	_ = json.Unmarshal(body, &p) //nolint:errcheck // tolerant: a decode error leaves p zero
	return p
}

// decodeStop decodes a Stop body into stopPayload. Stop is a Codex-only hook (it
// is Codex's per-turn end signal, standing in for the SessionEnd it lacks); the
// field names match the internal struct, so the decode is identity and the client
// parameter is kept for the uniform adapter surface. Tolerant: a decode error
// leaves the zero payload (no session id -> heartbeat/harvest both no-op).
func decodeStop(_ Client, body []byte) stopPayload {
	var p stopPayload
	_ = json.Unmarshal(body, &p) //nolint:errcheck // tolerant: a decode error leaves p zero
	return p
}

// decodeSubagentStart normalizes the captured Codex SubagentStart contract into
// subagentPayload. The event's session_id is the parent external session id;
// agent_id identifies the child. Field names currently match the internal wire
// tags, but keeping the decode in the client adapter gives future Codex renames a
// single home. Tolerant: malformed input leaves the zero payload; the handler
// then acknowledges it with empty context rather than guessing required fields.
func decodeSubagentStart(_ Client, body []byte) subagentPayload {
	var p subagentPayload
	_ = json.Unmarshal(body, &p) //nolint:errcheck // tolerant: a decode error leaves p zero
	return p
}

// decodeSubagentStop normalizes both clients' SubagentStop payloads. Current
// Codex carries the parent session/rollout, child rollout, turn, model, agent
// identity, and a stable last_assistant_message. Claude Code omits the Codex-only
// fields and continues through its existing planning-subagent capture behavior.
// Tolerant: malformed input yields a no-op acknowledgment.
func decodeSubagentStop(_ Client, body []byte) subagentPayload {
	var p subagentPayload
	_ = json.Unmarshal(body, &p) //nolint:errcheck // tolerant: a decode error leaves p zero
	return p
}
