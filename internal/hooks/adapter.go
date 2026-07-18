package hooks

import (
	"encoding/json"
	"io"
	"net/http"
)

// Codex's hook payloads differ from Claude Code's in a few field names (captured
// live in internal/hooks/testdata/codex and the codex-hook-contract-0-144-5
// memory). A per-client adapter decodes each raw hook body into the single
// internal struct the handlers already use, so project registration, briefing
// assembly, ambient sessions, and recall stay shared code with no per-client
// branches downstream. Claude Code is the identity adapter -- its payloads already
// ARE the internal shape -- so only Codex's renames live here.
//
// The client discriminator arrives OUT OF BAND, as the ?client= query param that
// `seam hook --client` appends, not in the body: the body decode is itself
// client-specific (Codex names the submitted prompt differently), so the handler
// must know the client BEFORE it can decode. An absent or unknown value resolves
// to Claude Code (normalizeClient), so every existing CC hook -- which sends no
// client and no query param -- is byte-for-byte unchanged.

// clientQueryParam is the query key `seam hook --client` sets on the forwarded
// request. It is the sole transport for the discriminator -- the body never
// carries it. cmd/seam keeps its own copy of this literal (it must not import
// this package, which would drag SQLite into a binary whose job is one POST); the
// two are trivially "client" and a mismatch just resolves to the Claude Code
// default, so no downstream breakage hides behind drift.
const clientQueryParam = "client"

// clientFromRequest reads the client discriminator from the request's ?client=
// query param, defaulting an absent or unknown value to Claude Code.
func clientFromRequest(r *http.Request) Client {
	return normalizeClient(r.URL.Query().Get(clientQueryParam))
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

// decodeSessionEnd decodes a SessionEnd body into endPayload. Codex 0.144.5 fires
// no SessionEnd (session end is reaper-driven off Stop -- design decision D5), so
// this path is Claude Code-only in practice; the decode is client-agnostic and the
// parameter is kept for the uniform adapter surface. Tolerant: a decode error
// leaves the zero payload (no session id -> no-op).
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
