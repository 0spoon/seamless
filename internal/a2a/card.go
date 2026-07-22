// Package a2a implements a minimal A2A (Agent2Agent) surface for seamlessd:
// the agent card at /.well-known/agent-card.json and a JSON-RPC endpoint at
// /api/a2a whose one skill is recall -- another agent on this machine sends a
// text message and gets the owner's fused memory search results back.
// Synchronous only: message/send replies with a completed Message, never a
// Task, so the task lifecycle, streaming, and push notifications are all
// deliberately absent and the card says so.
package a2a

import (
	"encoding/json"
	"fmt"
)

// ProtocolVersion is the A2A generation the endpoint speaks: the v0.3
// JSON-RPC binding, whose card names the endpoint in url/preferredTransport.
// The card additionally carries the newer draft's supportedInterfaces shape
// (the one the agent-readiness scanners key on), the same
// both-generations-in-one-document approach as the MCP server card.
const ProtocolVersion = "0.3.0"

// AgentName is the display name the card leads with. It matches the MCP
// initialize handshake's mcp.ServerName -- one install, one agent identity --
// pinned by a docsgen test rather than an import, because internal/a2a and
// internal/mcp are sibling API surfaces that do not import each other.
const AgentName = "Seamless"

// siteURL is the project site, used for provider and documentation links.
const siteURL = "https://thereisnospoon.org"

// agentDescription is the card's description. Like the MCP server card it
// must tell a reader who only sees the card that the endpoint is per-install
// localhost -- a crawler that dials the URL from anywhere else reaches its own
// machine, not a service.
const agentDescription = "Local-first shared memory and task coordination for AI coding agents. " +
	"The one A2A skill is recall: send a text query with message/send and the reply is a completed " +
	"message carrying the owner's fused memory and note search results. " +
	"The endpoint is per-install: each Seamless daemon serves A2A locally on the machine it runs on; " +
	"there is no hosted remote."

// AgentCard is the A2A agent card (v0.3 field set, plus the newer draft's
// supportedInterfaces). Exported so docsgen can render the site twin from the
// same struct the daemon serves -- one shape, no drift.
type AgentCard struct {
	ProtocolVersion    string                    `json:"protocolVersion"`
	Name               string                    `json:"name"`
	Description        string                    `json:"description"`
	URL                string                    `json:"url"`
	PreferredTransport string                    `json:"preferredTransport"`
	Provider           *AgentProvider            `json:"provider,omitempty"`
	Version            string                    `json:"version"`
	DocumentationURL   string                    `json:"documentationUrl,omitempty"`
	Capabilities       AgentCapabilities         `json:"capabilities"`
	SecuritySchemes    map[string]SecurityScheme `json:"securitySchemes,omitempty"`
	Security           []map[string][]string     `json:"security,omitempty"`
	DefaultInputModes  []string                  `json:"defaultInputModes"`
	DefaultOutputModes []string                  `json:"defaultOutputModes"`
	Skills             []AgentSkill              `json:"skills"`
	// SupportsAuthenticatedExtendedCard is explicit (not omitempty) so a
	// client never has to guess whether false means "no" or "not stated".
	SupportsAuthenticatedExtendedCard bool `json:"supportsAuthenticatedExtendedCard"`
	// SupportedInterfaces is the newer draft's interface declaration. Each
	// entry carries both that draft's protocolBinding and v0.3's transport
	// vocabulary, so either generation of client finds the field it expects.
	SupportedInterfaces []AgentInterface `json:"supportedInterfaces"`
}

type AgentProvider struct {
	Organization string `json:"organization"`
	URL          string `json:"url,omitempty"`
}

// AgentCapabilities are all explicit booleans for the same reason as
// SupportsAuthenticatedExtendedCard: absent-vs-false ambiguity costs a client
// a probe request.
type AgentCapabilities struct {
	Streaming              bool `json:"streaming"`
	PushNotifications      bool `json:"pushNotifications"`
	StateTransitionHistory bool `json:"stateTransitionHistory"`
}

type SecurityScheme struct {
	Type        string `json:"type"`
	Scheme      string `json:"scheme,omitempty"`
	Description string `json:"description,omitempty"`
}

type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Examples    []string `json:"examples,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

type AgentInterface struct {
	URL             string `json:"url"`
	Transport       string `json:"transport"`
	ProtocolBinding string `json:"protocolBinding"`
}

// Card builds the agent card for one endpoint and version. The daemon passes
// its build version and real bind address; docsgen passes the server.json
// version and the default address -- so the site twin and a default install's
// live card agree on everything a release can know in advance.
func Card(version, endpoint string) AgentCard {
	if version == "" {
		version = "0.0.0-dev"
	}
	return AgentCard{
		ProtocolVersion:    ProtocolVersion,
		Name:               AgentName,
		Description:        agentDescription,
		URL:                endpoint,
		PreferredTransport: "JSONRPC",
		Provider:           &AgentProvider{Organization: "0spoon", URL: siteURL},
		Version:            version,
		DocumentationURL:   siteURL + "/docs/",
		Capabilities:       AgentCapabilities{},
		SecuritySchemes: map[string]SecurityScheme{
			"bearer": {
				Type:   "http",
				Scheme: "bearer",
				Description: "The install's static bearer key (mcp.api_key in seamless.yaml) -- " +
					"the same credential every local surface shares. See " + siteURL + "/auth.md.",
			},
		},
		Security:           []map[string][]string{{"bearer": {}}},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"application/json", "text/plain"},
		Skills: []AgentSkill{{
			ID:   "recall",
			Name: "Recall",
			Description: "Search the owner's memories and notes by meaning and keyword (RRF-fused). " +
				"Send the query as a text part; optional message metadata scopes it " +
				"(project, scope: all|memories|notes, limit). The reply carries a data part " +
				"({\"hits\": [...]}) and a text summary of the same hits.",
			Tags:        []string{"memory", "search", "knowledge"},
			Examples:    []string{"What did we decide about the retry backoff?"},
			InputModes:  []string{"text/plain"},
			OutputModes: []string{"application/json", "text/plain"},
		}},
		SupportsAuthenticatedExtendedCard: false,
		SupportedInterfaces: []AgentInterface{
			{URL: endpoint, Transport: "JSONRPC", ProtocolBinding: "json-rpc"},
		},
	}
}

// CardJSON is the one rendering path for the card -- the daemon serves these
// bytes and docsgen commits them, so the two cannot format-drift.
func CardJSON(version, endpoint string) ([]byte, error) {
	raw, err := json.MarshalIndent(Card(version, endpoint), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("a2a.CardJSON: %w", err)
	}
	return append(raw, '\n'), nil
}
