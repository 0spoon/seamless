package main

import (
	"github.com/0spoon/seamless/internal/a2a"
	"github.com/0spoon/seamless/internal/config"
)

// agentCardPath is where the A2A Agent Card publishes, relative to the site
// root: /.well-known/agent-card.json, the RFC 8615 path the A2A spec
// recommends and the agent-readiness scanners probe. Like the MCP server card
// the .json extension gets application/json from GitHub Pages natively.
const agentCardPath = ".well-known/agent-card.json"

// agentCard renders the site twin of the card every install's daemon serves
// live at /.well-known/agent-card.json. Both come from a2a.CardJSON -- one
// shape, no drift. The twin carries the registry version (server.json) and the
// default bind address; a live card differs only where an install differs (its
// build version, a non-default addr:).
func agentCard(reg *registryMeta) ([]byte, error) {
	return a2a.CardJSON(reg.Version, "http://"+config.Defaults().Addr+"/api/a2a")
}
