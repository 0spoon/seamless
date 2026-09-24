package mcp_test

// Cross-package guard tests for the optional-feature registry, MCP half.
//
// internal/features is a leaf (it imports config and nothing else), so the
// assertions that tie a registry entry to a real MCP surface cannot live there.
// They live here, where importing both the registry and the tool catalog is
// legal. Each test below answers one question a future engineer will get wrong
// by adding a surface on one side only, and says so in its failure message.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/agentguide"
	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/features"
	mcpserver "github.com/0spoon/seamless/internal/mcp"
	"github.com/0spoon/seamless/internal/store"
)

// featuresAll builds a config with every registered feature switched the same
// way, so the assertions below scale to a second optional feature without a
// single edit. Never transcribe a feature list here: that is the drift the
// registry exists to prevent.
func featuresAll(on bool) config.Features {
	var c config.Features
	for _, f := range features.Registry() {
		f.Set(&c, on)
	}
	return c
}

// shippedDefaults tunes a test server to the config a FRESH INSTALLATION has --
// every optional feature at its registry default -- rather than the shared
// fixture's research-enabled one.
func shippedDefaults(c *mcpserver.Config) { c.Features = features.Defaults() }

// registryTools is every tool name the registry claims, in registry order, so
// failure output is deterministic.
func registryTools() []string {
	var names []string
	for _, f := range features.Registry() {
		names = append(names, f.Tools...)
	}
	return names
}

// Invariant 1 (and the ToolOwners half of invariant 2): every tool a feature
// claims is a tool this server actually serves, and no two features claim the
// same one.
//
// A registry entry naming a tool that no longer exists is silent: HiddenTools
// would hide a name nothing serves, the Settings card would promise to hide a
// tool that is not there, and the real tool -- renamed, not gated -- would stay
// exposed with the feature off.
func TestFeatureRegistry_ToolsExistInTheCatalog(t *testing.T) {
	catalog := make(map[string]bool, len(mcpserver.Catalog()))
	for _, tool := range mcpserver.Catalog() {
		catalog[tool.Name] = true
	}

	for _, f := range features.Registry() {
		for _, name := range f.Tools {
			require.True(t, catalog[name],
				"feature %q claims MCP tool %q, which is not in mcp.Catalog(): a renamed or removed tool "+
					"leaves the registry gating a name nothing serves while the real tool stays exposed. "+
					"Update the Tools list in internal/features/features.go to match registerTools.",
				f.Key, name)
		}
	}

	// ToolOwners is the map the tool filter is built from, so the no-double-claim
	// rule matters in its derived form: a doubly-claimed tool collapses to one
	// entry and would reappear only when BOTH owners are on.
	require.Len(t, features.ToolOwners(), len(registryTools()),
		"two features claim the same MCP tool, so features.ToolOwners() collapsed a claim: "+
			"give each tool exactly one owning feature in internal/features/features.go")
}

// Invariant 3: the static initialize instructions stay feature-neutral.
//
// This lives here rather than in internal/agentguide on purpose. agentguide is a
// leaf that imports nothing, and MCP initialize is where its prose meets the
// filtered tool list -- so internal/mcp, which already imports both, is the one
// place the two can be compared without giving the guidance package a dependency
// on the registry.
//
// A client reads MCPInstructions BEFORE (and regardless of whether) a tool is
// exposed: Codex consumes it as server-wide guidance while deciding what to
// call. Prose that names a gated tool therefore teaches a workflow the agent
// cannot perform on a fresh installation. Feature-specific instruction belongs on
// a dynamic surface -- the briefing, the skills the feature installs -- which is
// assembled per request and can see the feature state.
func TestAgentGuide_NamesNoGatedTool(t *testing.T) {
	surfaces := []struct {
		name string
		text string
	}{
		{"agentguide.MCPInstructions", agentguide.MCPInstructions},
		{"agentguide.RequiredToolNames()", strings.Join(agentguide.RequiredToolNames(), " ")},
		{"agentguide.RequiredWorkflowTerms()", strings.Join(agentguide.RequiredWorkflowTerms(), " ")},
	}

	owners := features.ToolOwners()
	for _, tool := range registryTools() {
		for _, surface := range surfaces {
			require.NotContains(t, surface.text, tool,
				"%s names %q, a tool owned by the optional %q feature, which ships OFF. "+
					"The static initialize instructions are read before any gate applies, so they must stay "+
					"feature-neutral: move the sentence to a dynamic surface (the briefing, or the feature's "+
					"skill) rather than teaching a workflow a fresh installation cannot run.",
				surface.name, tool, owners[tool])
		}
	}
}

// Invariant 6: the live tool surface is exactly the registered surface minus the
// tools of whatever is currently off -- derived from the registry, never from a
// transcribed count, so it keeps holding when a second optional feature lands.
//
// ToolCount and Catalog() are untouched throughout: they count REGISTRATION, and
// the gate is a filter over the registered list.
func TestFeatureGate_LiveToolCountDerivesFromTheRegistry(t *testing.T) {
	ctx := context.Background()
	require.NotEmpty(t, features.ToolOwners(),
		"no registered feature owns an MCP tool, so this guard proves nothing: "+
			"either a feature lost its Tools list or this test outlived its subject")

	url, db := newServerCfg(t, shippedDefaults)
	cli := dialClient(t, ctx, url, testKey)

	// A fresh installation: the shipped defaults hide their own tools and nothing
	// else.
	hidden := features.HiddenTools(features.Defaults())
	listed := listedTools(t, ctx, cli)
	require.Len(t, listed, mcpserver.ToolCount-len(hidden),
		"a fresh installation must advertise ToolCount minus the tools of the features that ship off "+
			"(%d - %d). A mismatch means a tool was registered without being counted, or a registry "+
			"Tools entry no longer matches a registered tool.", mcpserver.ToolCount, len(hidden))
	for _, name := range registryTools() {
		if !hidden[name] {
			continue
		}
		require.NotContains(t, listed, name,
			"%s belongs to a feature that is off by default but is still advertised: "+
				"the tool filter is not reading features.HiddenTools", name)
	}

	// Round trip, everything on: the full registered surface comes back.
	require.NoError(t, store.SetFeaturesConfig(ctx, db, featuresAll(true)))
	listed = listedTools(t, ctx, cli)
	require.Len(t, listed, mcpserver.ToolCount,
		"with every optional feature on, the live list must equal mcp.ToolCount -- "+
			"a gap means some tool is hidden by something other than an optional feature")
	for _, name := range registryTools() {
		require.Contains(t, listed, name,
			"%s stays hidden with its feature ON: enabling a feature must restore every tool it owns", name)
	}

	// And back off: the same connection loses exactly the registry's tools again.
	require.NoError(t, store.SetFeaturesConfig(ctx, db, featuresAll(false)))
	listed = listedTools(t, ctx, cli)
	require.Len(t, listed, mcpserver.ToolCount-len(registryTools()),
		"with every optional feature off, the live list must be ToolCount minus every registry tool")
	for _, name := range registryTools() {
		require.NotContains(t, listed, name,
			"%s survives its feature being switched back off: the gate must be symmetric", name)
	}
}
