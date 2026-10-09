package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/arctop/seamless/internal/a2a"
	"github.com/arctop/seamless/internal/config"
	seamlessmcp "github.com/arctop/seamless/internal/mcp"
)

// TestAgentCardMirrorsTheLiveSurface: the site twin must be exactly what a
// default install's daemon serves (same rendering path, server.json version,
// default bind address), carry the members the readiness scanners key on
// (name, version, description, supportedInterfaces, capabilities, skills with
// id/name/description), and agree with the sibling discovery surfaces on
// identity and host.
func TestAgentCardMirrorsTheLiveSurface(t *testing.T) {
	repoRoot(t)

	reg, err := loadRegistryMeta(serverJSONPath)
	require.NoError(t, err)
	raw, err := agentCard(reg)
	require.NoError(t, err)

	want, err := a2a.CardJSON(reg.Version, "http://"+config.Defaults().Addr+"/api/a2a")
	require.NoError(t, err)
	require.Equal(t, string(want), string(raw), "the twin is CardJSON's bytes verbatim")

	var card a2a.AgentCard
	require.NoError(t, json.Unmarshal(raw, &card))

	// One install, one agent identity: the A2A name is the MCP handshake name.
	require.Equal(t, seamlessmcp.ServerName, card.Name)
	require.Equal(t, reg.Version, card.Version, "the twin carries the registry version")
	require.NotEmpty(t, card.Description)

	require.Equal(t, "http://"+config.Defaults().Addr+"/api/a2a", card.URL)
	require.NotEmpty(t, card.SupportedInterfaces)
	for _, iface := range card.SupportedInterfaces {
		require.Equal(t, card.URL, iface.URL)
		require.NotEmpty(t, iface.ProtocolBinding)
	}
	require.NotEmpty(t, card.Skills)
	for _, sk := range card.Skills {
		require.NotEmpty(t, sk.ID)
		require.NotEmpty(t, sk.Name)
		require.NotEmpty(t, sk.Description)
	}

	// The provider is the publisher, Arctop, at its own site; the
	// documentation link stays on the canonical product host.
	require.NotNil(t, card.Provider)
	require.Equal(t, "Arctop", card.Provider.Organization)
	require.Equal(t, "https://arctop.com", card.Provider.URL)
	require.Equal(t, siteBaseURL+"/docs/", card.DocumentationURL)
}
