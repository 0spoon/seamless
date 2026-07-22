package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/0spoon/seamless/internal/config"
	seamlessmcp "github.com/0spoon/seamless/internal/mcp"
)

// serverJSONPath is the MCP registry listing at the repo root -- the one
// version file the release skill bumps. The server card is generated from it,
// so the registry listing and the published card cannot disagree about
// identity or version; a release bump makes the committed card stale, which
// docs-check catches until `make docs` is rerun.
const serverJSONPath = "server.json"

// serverCardPath is where the card publishes, relative to the site root:
// /.well-known/mcp/server-card.json, the primary path SEP-1649 scanners probe.
// The .json extension is deliberate -- GitHub Pages serves it as
// application/json natively, so unlike api-catalog no edge rule is needed.
const serverCardPath = ".well-known/mcp/server-card.json"

// registryMeta is the slice of server.json the card reuses. Only the fields
// the card serves are parsed; the registry $schema and anything it adds later
// pass through server.json untouched.
type registryMeta struct {
	Name        string        `json:"name"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Repository  *registryRepo `json:"repository"`
	WebsiteURL  string        `json:"websiteUrl"`
	Version     string        `json:"version"`
}

type registryRepo struct {
	URL    string `json:"url"`
	Source string `json:"source,omitempty"`
}

func loadRegistryMeta(path string) (*registryMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var reg registryMeta
	if err := json.Unmarshal(raw, &reg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if reg.Name == "" || reg.Version == "" || reg.Description == "" {
		return nil, fmt.Errorf("%s: name, version, and description are required (the server card serves them verbatim)", path)
	}
	return &reg, nil
}

// serverCardDoc is the published MCP Server Card (SEP-1649). The standard is
// still a draft and today's two consumers want two shapes, so one document
// carries both: the extension repository's schema is a strict subset of
// server.json (top-level name / version / description, remotes for hosted
// endpoints), while the deployed scanners key on an initialize-result shape
// (serverInfo, transport, capabilities). There is no $schema line because the
// draft's server-card schema URL is not published yet, and no remotes entry
// because Seamless has no hosted remote -- url names the endpoint every
// install serves on its own machine, and the description says so in prose.
type serverCardDoc struct {
	Name         string           `json:"name"`
	Title        string           `json:"title,omitempty"`
	Description  string           `json:"description"`
	Version      string           `json:"version"`
	WebsiteURL   string           `json:"websiteUrl,omitempty"`
	Repository   *registryRepo    `json:"repository,omitempty"`
	ServerInfo   cardServerInfo   `json:"serverInfo"`
	URL          string           `json:"url"`
	Transport    cardTransport    `json:"transport"`
	Capabilities cardCapabilities `json:"capabilities"`
}

type cardServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type cardTransport struct {
	Type string `json:"type"`
}

// cardCapabilities mirrors the ServerCapabilities the initialize handshake
// returns: tools only (no resources or prompts are registered), with
// listChanged false per WithToolCapabilities(false) in internal/mcp/server.go.
type cardCapabilities struct {
	Tools cardToolCapabilities `json:"tools"`
}

type cardToolCapabilities struct {
	ListChanged bool `json:"listChanged"`
}

// localEndpointNote is appended to the registry description: a crawler that
// reads only the card must learn that the URL is per-install localhost, not a
// hosted remote it can dial from wherever it is running.
const localEndpointNote = "The MCP endpoint is per-install: each Seamless daemon serves streamable HTTP locally on the machine it runs on; there is no hosted remote."

// mcpEndpoint is the streamable-HTTP endpoint every install serves, derived
// from the default bind address so a port change cannot leave the published
// card lying. The /api/mcp mount lives in cmd/seamlessd/main.go.
func mcpEndpoint() string {
	return "http://" + config.Defaults().Addr + "/api/mcp"
}

// serverCard renders the card from the registry listing. serverInfo mirrors
// the initialize handshake (mcp.ServerName plus the release version -- the
// build injects the same tag the registry listing carries), not the registry's
// reverse-DNS name, which lives at the top level.
func serverCard(reg *registryMeta) ([]byte, error) {
	doc := serverCardDoc{
		Name:         reg.Name,
		Title:        reg.Title,
		Description:  reg.Description + " " + localEndpointNote,
		Version:      reg.Version,
		WebsiteURL:   reg.WebsiteURL,
		Repository:   reg.Repository,
		ServerInfo:   cardServerInfo{Name: seamlessmcp.ServerName, Version: reg.Version},
		URL:          mcpEndpoint(),
		Transport:    cardTransport{Type: "streamable-http"},
		Capabilities: cardCapabilities{Tools: cardToolCapabilities{ListChanged: false}},
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal server card: %w", err)
	}
	return append(raw, '\n'), nil
}
