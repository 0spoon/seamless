package console

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
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

// The level POST's landing rule (T5 wires it): back where the owner was when
// the new level shows that screen, Home with a note when it hides it, and never
// anywhere off-console.
func TestRedirectAfterLevelChange(t *testing.T) {
	reg := screenRegistry()
	home := func(label, lvl string) string {
		return "/console/?notice=" + url.QueryEscape("Switched to "+lvl+". "+label+" is not in the "+lvl+
			" sidebar, so you are back on Home.")
	}
	for _, tc := range []struct {
		name, from string
		to         level
		want       string
	}{
		{"down past a hidden screen lands home", "/console/retrieval?w=7d", levelBasic, home("Retrieval", "Basic")},
		{"up keeps the page and its query", "/console/retrieval?w=7d", levelAdvanced,
			"/console/retrieval?notice=Switched+to+Advanced.&w=7d"},
		{"a detail page follows its screen", "/console/memories/01ABC", levelBasic,
			"/console/memories/01ABC?notice=Switched+to+Basic."},
		{"a detail page under a hidden screen lands home", "/console/tasks/01TASK", levelBasic, home("Tasks", "Basic")},
		{"no gated screen: stay", "/console/events/01XYZ", levelBasic, "/console/events/01XYZ?notice=Switched+to+Basic."},
		{"context is its own advanced screen", "/console/context", levelStandard, home("Context", "Standard")},
		{"standard keeps now", "/console/now", levelStandard, "/console/now?notice=Switched+to+Standard."},
		{"a stale flash never replays", "/console/sessions?error=old&notice=old", levelBasic,
			"/console/sessions?notice=Switched+to+Basic."},
		{"home stays home", "/console/", levelBasic, "/console/?notice=Switched+to+Basic."},
		{"off-console is refused", "https://evil.example/console/", levelBasic, "/console/?notice=Switched+to+Basic."},
		{"protocol-relative is refused", "//evil.example/console/", levelBasic, "/console/?notice=Switched+to+Basic."},
		{"empty means home", "", levelStandard, "/console/?notice=Switched+to+Standard."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, redirectAfterLevelChange(reg, tc.to, tc.from))
		})
	}
}

func TestScreenForPath(t *testing.T) {
	reg := screenRegistry()
	for path, want := range map[string]string{
		"/console/":                "overview",
		"/console/memories":        "memories",
		"/console/memories/01A":    "memories",
		"/console/labs/a/b":        "labs",
		"/console/settings":        "settings",
		"/console/interactionsX":   "",
		"/console/events/01E":      "",
		"/console/sessions/01S/x/": "sessions",
	} {
		sc, ok := screenForPath(reg, path)
		require.Equal(t, want != "", ok, path)
		require.Equal(t, want, sc.ID, path)
	}
}

// seedLevelFleet gives a console enough rows that a level-dependent answer
// would show: a project, a session, and a task and plan whose titles match
// "xylophone" -- work that only Standard's screens list.
func seedLevelFleet(t *testing.T, db *sql.DB) (taskID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, store.CreateProject(ctx, db, core.Project{
		ID: mustID(t), Slug: "orchestra", Name: "orchestra", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: mustID(t), Name: "cc/xylophone", ProjectSlug: "orchestra", Status: core.SessionActive,
		CreatedAt: now, UpdatedAt: now,
	}))
	taskID = mustID(t)
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: taskID, ProjectSlug: "orchestra", Title: "tune the xylophone", Status: core.TaskOpen,
		CreatedAt: now, UpdatedAt: now,
	}))
	return taskID
}

// setLevel stores level as the owner's choice, the way the Settings picker does.
func setLevel(t *testing.T, db *sql.DB, lvl string) {
	t.Helper()
	require.NoError(t, store.SetConsoleLevel(context.Background(), db, lvl, store.ConsoleLevelChosen))
}

// levelFields are the JSON keys that report the level itself: the one part of a
// JSON answer allowed to differ by level.
var levelFields = []string{"consoleLevel", "consoleLevelOverridden", "consoleLevelSource"}

// rawJSON fetches path as a bearer JSON caller and returns the decoded body
// minus the level-reporting fields.
func rawJSON(t *testing.T, mux *http.ServeMux, path string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("Accept", "application/json")
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code, "GET %s: %s", path, rr.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	for _, k := range levelFields {
		delete(out, k)
	}
	return out
}

// Principle 2: JSON callers are level-blind. The CLI, `seam doctor`, and
// client_features read these payloads, and a presentation choice in the console
// must never change what they get -- including the search groups for work only
// Standard's screens list.
func TestLevels_JSONAnswersAreIdenticalAcrossLevels(t *testing.T) {
	db, mux := newConsoleLevel(t, config.Features{Research: true}, "basic")
	seedLevelFleet(t, db)

	for _, path := range []string{
		"/console/?format=json",
		"/console/settings?format=json",
		"/console/search?q=xylophone&format=json",
		"/console/retrieval?format=json",
		"/console/sessions?format=json",
		"/console/tasks?format=json",
	} {
		var answers []map[string]any
		for _, lvl := range config.ConsoleLevels {
			setLevel(t, db, lvl)
			answers = append(answers, rawJSON(t, mux, path))
		}
		require.Equal(t, answers[0], answers[1], "%s: basic and standard JSON must be identical", path)
		require.Equal(t, answers[0], answers[2], "%s: basic and advanced JSON must be identical", path)
	}

	// The search answer really does carry the hidden scope's group at basic.
	setLevel(t, db, "basic")
	var data searchData
	getJSON(t, mux, "/console/search?q=xylophone&format=json", &data)
	var kinds []string
	for _, g := range data.Groups {
		kinds = append(kinds, g.Kind)
	}
	require.Contains(t, kinds, "tasks", "JSON search is level-blind: basic still gets the tasks group")
}

// Principle 2, the page half: a screen above the level renders in full at its
// URL, under a banner offering the switch -- never a 403, 404, or redirect.
func TestLevels_HiddenScreenRendersWithTheBanner(t *testing.T) {
	db, mux := newConsoleLevel(t, config.Features{}, "basic")

	rr := getPeek(t, mux, "/console/retrieval?w=7d")
	require.Equal(t, http.StatusOK, rr.Code)
	page := rr.Body.String()
	require.Contains(t, page, `data-level-banner="retrieval"`)
	require.Contains(t, page, "<strong>Retrieval</strong> is an Advanced screen. You are in Basic: it works, it just is not in your sidebar.")
	require.Contains(t, page, `<form method="post" action="/console/settings/level">`)
	require.Contains(t, page, `<input type="hidden" name="level" value="advanced">`)
	require.Contains(t, page, `<input type="hidden" name="return" value="/console/retrieval?w=7d">`)
	require.Contains(t, page, "Switch to Advanced")
	require.Contains(t, page, "data-level-banner-dismiss")
	require.Contains(t, page, `class="retrieval-page`, "the full screen renders beneath the banner")
	require.NotContains(t, page, `href="/console/retrieval" data-key`, "the screen is still not in the sidebar")

	// The banner is information, not an error: no role=alert, no flash hook.
	banner := regexp.MustCompile(`(?s)<div class="section notice level-banner"[^>]*>`).FindString(page)
	require.NotContains(t, banner, "alert")
	require.NotContains(t, banner, "data-flash")

	// A Standard screen at basic says "a Standard screen".
	require.Contains(t, getPeek(t, mux, "/console/tasks").Body.String(),
		"<strong>Tasks</strong> is a Standard screen. You are in Basic")

	// Context highlights Projects but is its own Advanced screen.
	setLevel(t, db, "standard")
	require.Contains(t, getPeek(t, mux, "/console/context").Body.String(), `data-level-banner="context"`)

	// At the screen's level there is nothing to say.
	setLevel(t, db, "advanced")
	for _, path := range []string{"/console/retrieval", "/console/context", "/console/tasks"} {
		require.NotContains(t, getPeek(t, mux, path).Body.String(), "data-level-banner", path)
	}
	// A screen every level shows never carries it.
	setLevel(t, db, "basic")
	require.NotContains(t, getPeek(t, mux, "/console/memories").Body.String(), "data-level-banner")
}

// A fragment is injected into a pane, never a page: it never carries the banner.
func TestLevels_FragmentsNeverCarryTheBanner(t *testing.T) {
	db, mux := newConsoleLevel(t, config.Features{}, "basic")
	taskID := seedLevelFleet(t, db)
	for _, path := range []string{
		"/console/tasks/" + taskID + "?peek=1",
		"/console/tasks/" + taskID + "?reader=1",
		"/console/projects/orchestra?peek=1",
	} {
		rr := getPeek(t, mux, path)
		require.Equal(t, http.StatusOK, rr.Code, path)
		require.NotContains(t, rr.Body.String(), "level-banner", path)
	}
	// The full task page does.
	require.Contains(t, getPeek(t, mux, "/console/tasks/"+taskID).Body.String(), `data-level-banner="tasks"`)
}

// Search offers only the level's scopes on the page, while a hidden scope still
// works by URL (with the banner) and a misspelling is still a named 400 naming
// the level-blind enum.
func TestLevels_SearchScopesFollowTheLevel(t *testing.T) {
	db, mux := newConsoleLevel(t, config.Features{Research: true}, "basic")
	seedLevelFleet(t, db)

	page := getPeek(t, mux, "/console/search?q=xylophone").Body.String()
	for _, scope := range []string{"all", "memories", "notes", "sessions"} {
		require.Contains(t, page, `href="/console/search?scope=`+scope+`&amp;`, "basic offers %s", scope)
	}
	for _, scope := range []string{"tasks", "plans", "trials", "projects"} {
		require.NotContains(t, page, `href="/console/search?scope=`+scope+`&amp;`, "basic hides %s", scope)
	}
	require.Contains(t, page, "cc/xylophone", "the session matches under all")
	require.NotContains(t, page, "tune the xylophone", `"all" on the basic page covers the offered scopes only`)
	require.Contains(t, page, `placeholder="Search memories, notes, and sessions&hellip;"`)
	require.Contains(t, page, `data-scopes="memories notes sessions"`, "the palette narrows to the same scopes")

	// Hidden, not locked: an explicit hidden scope still searches, under the banner.
	page = getPeek(t, mux, "/console/search?q=xylophone&scope=tasks").Body.String()
	require.Contains(t, page, "tune the xylophone")
	require.Contains(t, page, `data-level-banner="tasks"`)
	require.Contains(t, page, `href="/console/search?scope=tasks&amp;`, "the active scope stays visible in the selector")

	// A misspelling is refused with the level-blind enum, as before.
	rr := getPeek(t, mux, "/console/search?q=xylophone&scope=task")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "valid values are all, memories, notes, tasks, plans, trials, projects, sessions")

	// Standard offers the work scopes; the research scope follows its feature.
	setLevel(t, db, "standard")
	page = getPeek(t, mux, "/console/search?q=xylophone").Body.String()
	require.Contains(t, page, "tune the xylophone")
	for _, scope := range []string{"tasks", "plans", "trials", "projects"} {
		require.Contains(t, page, `href="/console/search?scope=`+scope+`&amp;`, "standard offers %s", scope)
	}
	require.NotContains(t, page, "data-level-banner")
}

// The project workspace's Interactions and Context tabs are those screens,
// scoped: they follow the screens' level, and a direct ?tab= still renders.
func TestLevels_ProjectTabsFollowTheirScreens(t *testing.T) {
	db, mux := newConsoleLevel(t, config.Features{}, "standard")
	seedLevelFleet(t, db)

	page := getPeek(t, mux, "/console/projects/orchestra").Body.String()
	require.Contains(t, page, `data-tab="memories"`)
	require.NotContains(t, page, `data-tab="interactions"`)
	require.NotContains(t, page, `data-tab="context"`)
	require.NotContains(t, page, "data-level-banner", "projects is a standard screen")

	page = getPeek(t, mux, "/console/projects/orchestra?tab=interactions").Body.String()
	require.Contains(t, page, `data-level-banner="interactions"`)
	require.Contains(t, page, `data-tab="interactions"`, "the requested tab stays in the bar, marked on")
	require.Contains(t, page, `data-panel="interactions"`)

	setLevel(t, db, "advanced")
	page = getPeek(t, mux, "/console/projects/orchestra").Body.String()
	require.Contains(t, page, `data-tab="interactions"`)
	require.Contains(t, page, `data-tab="context"`)
	require.NotContains(t, page, "data-level-banner")
}
