package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	seamlessmcp "github.com/0spoon/seamless/internal/mcp"
)

// TestServerCardMirrorsServerJSON: the card is generated from the repo-root
// server.json, and this pins the mirror -- identity and version verbatim, the
// handshake serverInfo, and the members the SEP-1649 scanners key on
// (serverInfo, url, transport, capabilities). An agent that trusts the card
// connects blind, so the endpoint must be the one every install actually
// serves and the description must say it is per-install localhost.
func TestServerCardMirrorsServerJSON(t *testing.T) {
	repoRoot(t)

	reg, err := loadRegistryMeta(serverJSONPath)
	require.NoError(t, err)
	raw, err := serverCard(reg)
	require.NoError(t, err)

	var doc struct {
		Name        string        `json:"name"`
		Title       string        `json:"title"`
		Description string        `json:"description"`
		Version     string        `json:"version"`
		WebsiteURL  string        `json:"websiteUrl"`
		Repository  *registryRepo `json:"repository"`
		ServerInfo  struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		URL       string `json:"url"`
		Transport struct {
			Type string `json:"type"`
		} `json:"transport"`
		Capabilities struct {
			Tools *struct {
				ListChanged bool `json:"listChanged"`
			} `json:"tools"`
		} `json:"capabilities"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))

	require.Equal(t, reg.Name, doc.Name)
	require.Equal(t, reg.Title, doc.Title)
	require.Equal(t, reg.Version, doc.Version)
	require.Equal(t, reg.WebsiteURL, doc.WebsiteURL)
	require.Equal(t, siteBaseURL, doc.WebsiteURL,
		"the card publishes on the site it names; if server.json moves hosts, so must siteBaseURL")
	require.NotNil(t, doc.Repository)
	require.Equal(t, reg.Repository.URL, doc.Repository.URL)
	require.True(t, strings.HasPrefix(doc.Description, reg.Description+" "),
		"the description leads with the registry description verbatim")
	require.Contains(t, doc.Description, "per-install",
		"the card must say the endpoint is per-install, not a hosted remote")

	require.Equal(t, seamlessmcp.ServerName, doc.ServerInfo.Name,
		"serverInfo mirrors the initialize handshake, not the registry reverse-DNS name")
	require.Equal(t, reg.Version, doc.ServerInfo.Version)
	require.Equal(t, "http://"+config.Defaults().Addr+"/api/mcp", doc.URL,
		"the endpoint is the default bind address plus the /api/mcp mount")
	require.Equal(t, "streamable-http", doc.Transport.Type)
	require.NotNil(t, doc.Capabilities.Tools,
		"capabilities declares tools -- the only primitive the server registers")
	require.False(t, doc.Capabilities.Tools.ListChanged,
		"mirrors WithToolCapabilities(false) in internal/mcp/server.go")
}

// TestLoadRegistryMetaRejectsIncomplete: the card serves name, version, and
// description verbatim, so a listing missing any of them must fail the load
// rather than publish a hollow card.
func TestLoadRegistryMetaRejectsIncomplete(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing-name", `{"version":"1.0.0","description":"d"}`},
		{"missing-version", `{"name":"n","description":"d"}`},
		{"missing-description", `{"name":"n","version":"1.0.0"}`},
		{"not-json", `{`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "server.json")
			require.NoError(t, os.WriteFile(path, []byte(tt.body), 0o644))
			_, err := loadRegistryMeta(path)
			require.Error(t, err)
		})
	}
}
