package console

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
)

// A level change reads as one ledger line in Activity, like a features toggle;
// a reset names the level it fell back to, and a thin payload says less rather
// than inventing a level.
func TestEventSummary_LevelChanged(t *testing.T) {
	for _, tc := range []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{"from": "basic", "to": "standard", "by": "console"}, "console level basic -> standard"},
		{map[string]any{"to": "advanced", "by": "console"}, "console level set to advanced"},
		{map[string]any{"from": "advanced", "to": "basic", "reset": true}, "console level reset to the file configuration (basic)"},
		{map[string]any{"reset": true}, "console level reset to the file configuration"},
		{nil, "console level changed"},
	} {
		require.Equal(t, tc.want, eventSummary(core.Event{Kind: eventLevelChanged, Payload: tc.payload}))
	}
	require.Equal(t, "Console level changed", evtLabel(string(eventLevelChanged)))
}

// The typed levels are config.ConsoleLevels by index: the constants, the
// parse, and the names must never drift apart.
func TestLevels_MatchConfig(t *testing.T) {
	require.Equal(t, []string{"basic", "standard", "advanced"}, config.ConsoleLevels)
	require.Len(t, allLevels(), len(config.ConsoleLevels))
	for i, name := range config.ConsoleLevels {
		lvl, err := parseLevel(name)
		require.NoError(t, err)
		require.Equal(t, level(i), lvl)
		require.Equal(t, name, lvl.String())
	}
	require.Equal(t, levelBasic, mustLevel(t, "basic"))
	require.Equal(t, levelStandard, mustLevel(t, "standard"))
	require.Equal(t, levelAdvanced, mustLevel(t, "advanced"))
	require.Equal(t, "Standard", levelStandard.Label())

	_, err := parseLevel("expert")
	require.ErrorContains(t, err, "valid values are basic, standard, advanced")

	ok, err := levelStandard.AtLeast("basic")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = levelStandard.AtLeast("advanced")
	require.NoError(t, err)
	require.False(t, ok)
	_, err = levelAdvanced.AtLeast("standrd")
	require.Error(t, err, "a typo'd template gate must fail the render, not hide the markup")
}

func mustLevel(t *testing.T, name string) level {
	t.Helper()
	lvl, err := parseLevel(name)
	require.NoError(t, err)
	return lvl
}

// The registry is well formed: unique ids and chords, known levels and badges,
// and a nav row for everything but the palette-only pages.
func TestScreenRegistry_WellFormed(t *testing.T) {
	ids, keys := map[string]bool{}, map[string]bool{}
	for _, sc := range screenRegistry() {
		require.NotEmpty(t, sc.ID)
		require.False(t, ids[sc.ID], "duplicate screen id %q", sc.ID)
		ids[sc.ID] = true
		require.NotEmpty(t, sc.Label, "%s needs a label", sc.ID)
		require.NotEmpty(t, string(icon(sc.Icon)), "%s names icon %q, which the icon set does not have", sc.ID, sc.Icon)
		require.True(t, strings.HasPrefix(sc.Href, "/console/"), "%s href %q", sc.ID, sc.Href)
		require.Contains(t, allLevels(), sc.Min, "%s has an unknown minimum level", sc.ID)
		if sc.Badge != "" {
			_, ok := navCounts{}.count(sc.Badge)
			require.True(t, ok, "%s shows badge %q, which no navCounts field answers", sc.ID, sc.Badge)
		}
		if sc.NavRow {
			require.NotEmpty(t, sc.Key, "nav entry %s needs a g-chord", sc.ID)
			require.False(t, keys[sc.Key], "g-chord %q is bound twice", sc.Key)
			keys[sc.Key] = true
		} else {
			require.NotEmpty(t, sc.Hint, "palette-only page %s needs a one-line hint", sc.ID)
			require.Empty(t, sc.Key, "palette-only page %s has no nav link to carry a chord", sc.ID)
		}
	}
	// The existing chords survive the move to the registry.
	require.Equal(t, map[string]bool{"o": true, "n": true, "i": true, "m": true, "e": true, "r": true, "g": true,
		"w": true, "p": true, "t": true, "s": true, "l": true, "x": true, ",": true}, keys)
}

// Monotonic: each level shows everything the previous one does, with every
// feature state -- a Basic user who steps up finds everything where it was.
func TestScreenRegistry_VisibleSetsNest(t *testing.T) {
	for _, feats := range []config.Features{featuresAll(false), featuresAll(true)} {
		var prev []screen
		for _, lvl := range allLevels() {
			cur := visibleScreens(feats, lvl)
			for _, sc := range prev {
				require.Contains(t, cur, sc, "%s shows %s but %s does not", lvl-1, sc.ID, lvl)
			}
			prev = cur
		}
	}
	// And the matrix's anchors: basic is the six essentials, advanced is all.
	var basic []string
	for _, sc := range visibleScreens(featuresAll(true), levelBasic) {
		basic = append(basic, sc.ID)
	}
	require.Equal(t, []string{"overview", "memories", "notes", "gardener", "sessions", "settings", "search"}, basic)
	require.Len(t, visibleScreens(featuresAll(true), levelAdvanced), len(screenRegistry()))
}

// The banner names a screen above the level, and nothing else.
func TestLevelBannerFor(t *testing.T) {
	b := levelBannerFor("retrieval", levelBasic)
	require.NotNil(t, b)
	require.Equal(t, "Retrieval", b.Label)
	require.Equal(t, levelAdvanced, b.Min)
	require.Equal(t, levelBasic, b.Current)

	require.Nil(t, levelBannerFor("retrieval", levelAdvanced))
	require.Nil(t, levelBannerFor("memories", levelBasic))
	require.Nil(t, levelBannerFor("no-such-screen", levelBasic))
	require.NotNil(t, levelBannerFor("context", levelStandard), "Context is its own advanced screen")
}
