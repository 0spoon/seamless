// Package features is the registry of OPTIONAL features: the parts of Seamless
// the owner can switch on and off from the console without losing any data.
//
// It is the single source of truth every surface derives from -- the MCP tool
// filter, the console route gate and nav, the Settings cards, the docs, and the
// guard tests. Adding a future optional feature is one registry entry plus one
// config.Features field plus tagging its surfaces; nothing else hand-maintains a
// list of what a feature covers.
//
// The package is a leaf: it imports config and nothing else, so mcp, console,
// store, skills, and cmd/ can all import it without a cycle. The
// cross-consistency tests (registry tools exist in the MCP catalog, route
// prefixes are registered routes, nav ids exist in the layout) therefore live in
// those packages, where the imports are legal.
//
// Optional features default to OFF: a fresh installation exposes none of them
// until the owner enables it. Disabling never deletes anything -- it gates
// exposure only, and re-enabling restores every surface instantly.
package features

import (
	"slices"

	"github.com/0spoon/seamless/internal/config"
)

// Key identifies an optional feature. It is the stable identifier used by the
// config field, the settings form, and the URL fragment on the Settings page.
type Key string

// Research covers the whole research-lab domain: both console screens (Labs,
// Trials) and all three MCP tools. Labs and trials are one feature because a lab
// has no independent existence -- there is no labs table and no Lab core type, a
// lab is only the label its trials carry -- so splitting them would leave either
// a fully usable feature or an empty screen.
const Research Key = "research"

// Momentum covers the gentle motivation surfaces woven into EXISTING screens
// rather than screens of their own: the plan finish-line card (and its agent
// briefing emphasis), the activity calendar with capture streaks, knowledge
// payoff moments, and project maturity stages. They are one feature because
// they answer one question -- "is this knowledge practice building on
// itself?" -- and an owner who finds that framing noisy switches all of it
// off with one toggle.
const Momentum Key = "momentum"

// Feature describes one optional feature and every surface it owns.
type Feature struct {
	// Key is the stable identifier (also the settings anchor and form field).
	Key Key
	// Label is the owner-facing name, shown in Settings.
	Label string
	// Blurb is one sentence explaining what the feature is.
	Blurb string
	// Tools are the MCP tool names hidden from tools/list and rejected by
	// tools/call while the feature is off. Every name must exist in
	// mcp.Catalog() (asserted by a guard test in internal/mcp).
	Tools []string
	// NavIDs are the console sidebar nav entries to hide.
	NavIDs []string
	// RoutePrefixes are the console route prefixes to gate.
	RoutePrefixes []string
	// Surfaces are owner-facing phrases for the feature's IN-PAGE surfaces --
	// elements woven into existing screens (an Overview card, a board glyph)
	// rather than whole screens, gated where they render via the console's
	// per-request features state rather than by a route prefix. Each phrase
	// joins the Settings card's "what disappears" line verbatim, so register a
	// surface's phrase in the same change that adds the surface.
	Surfaces []string
	// Skill is the client-side skill that documents the feature's tools, or ""
	// when it has none. install-hooks skips installing it while the feature is
	// off, so agents never read about tools they cannot call.
	Skill string
	// Default is the built-in value, mirroring config.Defaults(). Optional
	// features ship off.
	Default bool

	// get and set are the accessors for this feature's config.Features field.
	// They keep the registry entry the only place a new feature is wired up:
	// every consumer goes through Feature.Enabled / Feature.Set rather than a
	// switch that would drift as features are added.
	get func(config.Features) bool
	set func(*config.Features, bool)
}

// Enabled reports whether this feature is on in the given resolved features
// config.
func (f Feature) Enabled(c config.Features) bool {
	if f.get == nil {
		return f.Default
	}
	return f.get(c)
}

// Set writes this feature's state into c. The console settings form rebuilds the
// whole config.Features from checkbox presence through it.
func (f Feature) Set(c *config.Features, on bool) {
	if f.set == nil || c == nil {
		return
	}
	f.set(c, on)
}

// registry is the ordered feature list. Order is stable and is the order the
// Settings cards render in.
var registry = []Feature{
	{
		Key:           Research,
		Label:         "Research labs & trials",
		Blurb:         "Systematic investigations recorded as trials -- expected vs actual, shared across agents working the same lab.",
		Tools:         []string{"lab_open", "trial_record", "trial_query"},
		NavIDs:        []string{"labs", "trials"},
		RoutePrefixes: []string{"/console/labs", "/console/trials"},
		Skill:         "seam-research",
		Default:       false,
		get:           func(c config.Features) bool { return c.Research },
		set:           func(c *config.Features, on bool) { c.Research = on },
	},
	{
		Key:   Momentum,
		Label: "Momentum",
		Blurb: "Gentle progress cues on existing screens -- plan finish lines, capture streaks, knowledge payoffs, and project growth -- judged from real activity, never invented.",
		Surfaces: []string{
			"the plan finish-line cards on Overview (and their agent-briefing emphasis)",
			"the capture calendar and streak on Sessions",
		},
		Default: false,
		get:     func(c config.Features) bool { return c.Momentum },
		set:     func(c *config.Features, on bool) { c.Momentum = on },
	},
}

// Registry returns the ordered optional features. The slice is a copy, so a
// caller cannot mutate the registry.
func Registry() []Feature { return slices.Clone(registry) }

// Get returns the feature with the given key. found is false for an unknown key.
func Get(key Key) (Feature, bool) {
	for _, f := range registry {
		if f.Key == key {
			return f, true
		}
	}
	return Feature{}, false
}

// Enabled reports whether the named feature is on. An unknown key is off: a
// caller asking about a feature that does not exist must not be told it is
// available.
func Enabled(c config.Features, key Key) bool {
	f, ok := Get(key)
	if !ok {
		return false
	}
	return f.Enabled(c)
}

// ToolOwners maps every registered optional tool name to the feature that owns
// it, regardless of whether that feature is on. Guard tests use it to assert no
// tool is claimed twice and that every name exists in the MCP catalog.
func ToolOwners() map[string]Key {
	owners := make(map[string]Key)
	for _, f := range registry {
		for _, tool := range f.Tools {
			owners[tool] = f.Key
		}
	}
	return owners
}

// HiddenTools returns the set of MCP tool names hidden by the currently disabled
// features. An empty result means every registered tool is exposed.
func HiddenTools(c config.Features) map[string]bool {
	hidden := make(map[string]bool)
	for _, f := range registry {
		if f.Enabled(c) {
			continue
		}
		for _, tool := range f.Tools {
			hidden[tool] = true
		}
	}
	return hidden
}

// Defaults returns the features config with every feature at its registry
// default. It mirrors config.Defaults().Features; a test asserts they agree.
func Defaults() config.Features {
	var c config.Features
	for _, f := range registry {
		f.Set(&c, f.Default)
	}
	return c
}
