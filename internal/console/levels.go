package console

// Console experience levels (plan:console-levels).
//
// The level decides how much of the console the owner SEES: basic (the fewest
// screens and knobs), standard (adds following the work), advanced (every
// screen and knob). It is presentation only -- agents, hooks, MCP tools, the
// briefing, the gardener, and recall are identical at every level -- so it
// lives here, in the console, and nowhere an agent can reach.
//
// Three rules shape everything that reads it:
//
//   - Hidden, not locked. A screen above the level leaves the sidebar, the
//     palette, and the shortcut map, but its URL keeps working (with a soft
//     banner offering the switch), and JSON callers never see a difference.
//   - Monotonic. Each level shows everything the previous one does; the
//     registry guard tests assert the nesting.
//   - Failure-soft, like features. The stored level is resolved live per
//     request, and a corrupt row logs and degrades to the file/env base rather
//     than taking the console down over presentation state.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/store"
)

// level is a console experience level. Its value is the index into
// config.ConsoleLevels, so comparison is nesting: a higher level shows
// everything a lower one does.
type level int

// The levels, in config.ConsoleLevels order (a guard test pins the two
// together).
const (
	levelBasic level = iota
	levelStandard
	levelAdvanced
)

// allLevels lists every level, fewest surfaces first.
func allLevels() []level {
	out := make([]level, len(config.ConsoleLevels))
	for i := range out {
		out[i] = level(i)
	}
	return out
}

// parseLevel reads a level name. An unknown name is an error naming the valid
// values -- never a silent default.
func parseLevel(name string) (level, error) {
	i := slices.Index(config.ConsoleLevels, name)
	if i < 0 {
		return levelBasic, fmt.Errorf("unknown console level %q: valid values are %s",
			name, strings.Join(config.ConsoleLevels, ", "))
	}
	return level(i), nil
}

// String is the level's config key ("basic").
func (l level) String() string {
	if l < 0 || int(l) >= len(config.ConsoleLevels) {
		return fmt.Sprintf("level(%d)", int(l))
	}
	return config.ConsoleLevels[l]
}

// Label is the owner-facing name ("Basic").
func (l level) Label() string { return titleWord(l.String()) }

// MarshalText makes a level encode as its name wherever it reaches JSON.
func (l level) MarshalText() ([]byte, error) { return []byte(l.String()), nil }

// AtLeast reports whether this level shows what a level named name shows. It
// is the ONE template gate for level-dependent markup ({{if $.Level.AtLeast
// "standard"}}). An unknown name is an error, which fails the render loudly
// rather than silently hiding the markup behind a typo.
func (l level) AtLeast(name string) (bool, error) {
	want, err := parseLevel(name)
	if err != nil {
		return false, err
	}
	return l >= want, nil
}

// levelState is the level in force for one request, with the facts the
// Settings precedence line and the Home welcome card read.
type levelState struct {
	Level level
	// Overridden reports a stored row setting the level (else file/env).
	Overridden bool
	// Source says who set a stored level: store.ConsoleLevelChosen or
	// store.ConsoleLevelSeeded ("" when not overridden).
	Source string
	// Welcomed reports whether the owner has seen the Home welcome card.
	Welcomed bool
}

// consoleLevel resolves the level for this request: the file/env base with the
// stored row layered over it. It returns no error on purpose, exactly like
// featuresConfig: a corrupt row is logged and degrades to the base. The
// degraded state counts as welcomed -- a row exists, so this is not a fresh
// installation, and re-asking on every Home load would nag about a fault the
// owner cannot see; choosing a level in Settings rewrites the row.
func (s *Service) consoleLevel(ctx context.Context) levelState {
	name, overridden, source, welcomed, err := store.ConsoleLevel(ctx, s.cfg.DB, s.baseLevel.String())
	if err != nil {
		s.logger.Warn("console: console level", "error", err)
		return levelState{Level: s.baseLevel, Welcomed: true}
	}
	lvl, err := parseLevel(name)
	if err != nil {
		// Unreachable while the store validates stored levels; kept so a
		// future store change cannot turn into a wrong level on screen.
		s.logger.Warn("console: console level", "error", err)
		return levelState{Level: s.baseLevel, Welcomed: true}
	}
	return levelState{Level: lvl, Overridden: overridden, Source: source, Welcomed: welcomed}
}
