package a2a

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testEndpoint = "http://127.0.0.1:8081/api/a2a"

// TestCard_DeclaresBothInterfaceGenerations: the card carries the v0.3 fields
// (url, preferredTransport) and the newer draft's supportedInterfaces, and the
// two must name the same endpoint -- a client of either generation connects to
// the same place.
func TestCard_DeclaresBothInterfaceGenerations(t *testing.T) {
	card := Card("1.2.3", testEndpoint)

	require.Equal(t, ProtocolVersion, card.ProtocolVersion)
	require.Equal(t, testEndpoint, card.URL)
	require.Equal(t, "JSONRPC", card.PreferredTransport)
	require.Len(t, card.SupportedInterfaces, 1)
	require.Equal(t, testEndpoint, card.SupportedInterfaces[0].URL)
	require.Equal(t, "JSONRPC", card.SupportedInterfaces[0].Transport)
	require.Equal(t, "json-rpc", card.SupportedInterfaces[0].ProtocolBinding)
}

// TestCard_CapabilitiesMatchTheImplementation: every capability the handler
// does not implement must be declared false -- a card that advertises
// streaming this server 404s on is the fake-endpoint pattern this repo
// refuses elsewhere.
func TestCard_CapabilitiesMatchTheImplementation(t *testing.T) {
	card := Card("1.2.3", testEndpoint)

	require.False(t, card.Capabilities.Streaming)
	require.False(t, card.Capabilities.PushNotifications)
	require.False(t, card.Capabilities.StateTransitionHistory)
	require.False(t, card.SupportsAuthenticatedExtendedCard)

	require.Len(t, card.Skills, 1, "recall is the only skill")
	sk := card.Skills[0]
	require.Equal(t, "recall", sk.ID)
	require.NotEmpty(t, sk.Name)
	require.NotEmpty(t, sk.Description)
	require.NotEmpty(t, sk.Tags)

	require.Contains(t, card.SecuritySchemes, "bearer")
	require.Equal(t, "http", card.SecuritySchemes["bearer"].Type)
	require.Equal(t, "bearer", card.SecuritySchemes["bearer"].Scheme)

	require.Contains(t, card.Description, "per-install",
		"the card must say the endpoint is per-install, not a hosted remote")
}

func TestCard_VersionFallback(t *testing.T) {
	require.Equal(t, "0.0.0-dev", Card("", testEndpoint).Version)
	require.Equal(t, "9.9.9", Card("9.9.9", testEndpoint).Version)
}

// TestCardJSON_RoundTripsAndTerminates: the emitted bytes are the committed
// site twin and the daemon's live response, so they must parse back to the
// same card and end in exactly one newline.
func TestCardJSON_RoundTripsAndTerminates(t *testing.T) {
	raw, err := CardJSON("1.2.3", testEndpoint)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(string(raw), "}\n"))

	var card AgentCard
	require.NoError(t, json.Unmarshal(raw, &card))
	require.Equal(t, Card("1.2.3", testEndpoint), card)
}
