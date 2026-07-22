package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// loadRepoScenarios loads the real scenes.js + docs-src framing pair.
func loadRepoScenarios(t *testing.T) []*Scenario {
	t.Helper()
	scenes, err := loadScenes(scenesPath)
	require.NoError(t, err)
	scenarios, err := loadScenarios("docs-src", scenes)
	require.NoError(t, err)
	return scenarios
}

// loadRepoSite loads the real docs-src site with its scenarios attached -- the
// shape run() builds. Tests that renderPages the repo site need this: docs
// prose links to /scenarios/ pages, and checkLinks resolves those against
// site.Scenarios.
func loadRepoSite(t *testing.T) *Site {
	t.Helper()
	site, err := loadSite("docs-src")
	require.NoError(t, err)
	site.Scenarios = loadRepoScenarios(t)
	reg, err := loadRegistryMeta(serverJSONPath)
	require.NoError(t, err)
	site.ServerCard, err = serverCard(reg)
	require.NoError(t, err)
	site.AgentCard, err = agentCard(reg)
	require.NoError(t, err)
	return site
}

// TestLoadScenesParsesTheCommittedData: the scenes.js wrapper is hand-authored
// JS around a JSON array; if its shape drifts, the scenario pages vanish, so
// the parse itself is pinned.
func TestLoadScenesParsesTheCommittedData(t *testing.T) {
	repoRoot(t)

	scenes, err := loadScenes(scenesPath)
	require.NoError(t, err)
	require.Len(t, scenes, 4, "the landing page plays four scenes")
	var ids []string
	for _, s := range scenes {
		ids = append(ids, s.ID)
	}
	require.ElementsMatch(t, []string{"cold-start", "hardening-trap", "token-safety", "coordination"}, ids)
}

// TestScenarioFramingCoversEveryScene: a scene with no framing file, or a
// framing file naming a ghost scene, must fail the build -- otherwise a fifth
// scene publishes on the landing page while /scenarios/ silently stays at four.
func TestScenarioFramingCoversEveryScene(t *testing.T) {
	repoRoot(t)

	scenarios := loadRepoScenarios(t)
	require.Len(t, scenarios, 4)
	var slugs []string
	for _, s := range scenarios {
		slugs = append(slugs, s.Slug)
	}
	require.Equal(t, []string{"cold-start", "constraint-violation", "token-safety", "task-collision"}, slugs,
		"pages in authored order")

	scenes, err := loadScenes(scenesPath)
	require.NoError(t, err)
	_, err = loadScenarios("docs-src", scenes[:2])
	require.ErrorContains(t, err, "is not in", "a framing file for an unknown scene fails")
	_, err = loadScenarios(t.TempDir(), scenes)
	require.Error(t, err, "scenes with no framing dir fail")
}

// TestScenarioOutcomesAppearVerbatim is the same promise site-check assertion 9
// makes for the landing page fallbacks: the outcome strings ARE the value
// claims, and the published pages must carry them exactly as recorded.
func TestScenarioOutcomesAppearVerbatim(t *testing.T) {
	repoRoot(t)

	files := renderRepoSite(t)
	for _, s := range loadRepoScenarios(t) {
		page, ok := files["scenarios/"+s.Slug+"/index.html"]
		require.True(t, ok, "scenario page %s is emitted", s.Slug)
		for _, p := range s.Scene.Panes {
			require.Contains(t, page, sceneEsc(p.Outcome),
				"%s must quote the %q outcome verbatim (HTML-escaped)", s.Slug, p.Key)
			require.Contains(t, page, sceneEsc(p.Source),
				"%s must name the recorded session id for pane %q", s.Slug, p.Key)
		}
		require.Contains(t, page, sceneEsc(s.Scene.Prompt), "%s shows the prompt", s.Slug)
	}
}

// TestScenarioTranscriptsAreComplete: every transcript step's text must reach
// the page. A silently dropped step role would pass every other check while
// publishing a transcript with holes.
func TestScenarioTranscriptsAreComplete(t *testing.T) {
	repoRoot(t)

	files := renderRepoSite(t)
	for _, s := range loadRepoScenarios(t) {
		page := files["scenarios/"+s.Slug+"/index.html"]
		for _, p := range s.Scene.Panes {
			for _, step := range p.Steps {
				switch step.Role {
				case "user", "ffwd", "comment", "cmd":
					require.Contains(t, page, sceneEsc(step.Text), "%s: %s step text missing", s.Slug, step.Role)
				case "tool":
					require.Contains(t, page, sceneEsc(step.Label), "%s: tool label missing", s.Slug)
					if step.Result != "" {
						require.Contains(t, page, sceneEsc(step.Result), "%s: tool result missing", s.Slug)
					}
				case "inject":
					// The injection body is focus-highlighted, so spot-check an
					// unhighlighted line survives whole.
					require.Contains(t, page, "Recall on demand with recall", "%s: inject body missing", s.Slug)
				case "fm":
					require.Contains(t, page, sceneEsc(step.V), "%s: fm value missing", s.Slug)
				}
			}
		}
	}
}

// TestScenarioHeadAndJSONLD mirrors the seo_test coverage for docs pages:
// canonical matches the page's own path, the social set is present, and the
// JSON-LD round-trips.
func TestScenarioHeadAndJSONLD(t *testing.T) {
	repoRoot(t)

	files := renderRepoSite(t)
	for _, s := range loadRepoScenarios(t) {
		page := files["scenarios/"+s.Slug+"/index.html"]
		require.Contains(t, page, `<link rel="canonical" href="`+s.Canonical()+`">`)
		require.Contains(t, page, `<meta property="og:url" content="`+s.Canonical()+`">`)
		require.Contains(t, page, `<meta property="og:type" content="article">`)
		require.Contains(t, page, `<meta name="twitter:card" content="summary_large_image">`)

		start := strings.Index(page, `<script type="application/ld+json">`)
		require.GreaterOrEqual(t, start, 0, "%s carries a JSON-LD block", s.Slug)
		payload := page[start+len(`<script type="application/ld+json">`):]
		payload = payload[:strings.Index(payload, "</script>")]
		var doc struct {
			Graph []map[string]any `json:"@graph"`
		}
		require.NoError(t, json.Unmarshal([]byte(payload), &doc), "%s JSON-LD parses", s.Slug)
		require.Len(t, doc.Graph, 2)
		require.Equal(t, "TechArticle", doc.Graph[1]["@type"])
		require.Equal(t, s.Canonical(), doc.Graph[1]["url"])
		require.NotContains(t, payload, "datePublished")
	}
}

// TestSitemapAndLlmsTxtListScenarioPages: the crawler files are how these pages
// get found at all -- nothing in the docs nav links them.
func TestSitemapAndLlmsTxtListScenarioPages(t *testing.T) {
	repoRoot(t)

	files := renderRepoSite(t)
	for _, s := range loadRepoScenarios(t) {
		require.Contains(t, files["sitemap.xml"], "<loc>"+s.Canonical()+"</loc>")
		require.Contains(t, files["llms.txt"], "("+s.Canonical()+")")
	}
	require.Contains(t, files["llms.txt"], "## Scenarios")
}

// TestSceneMarkdownMirrorsThePlayer pins the Go port of scenes-player.js's
// markdown-lite: same input, same shape -- headings flatten to h4, lists and
// blockquotes work, inline code/strong/em render.
func TestSceneMarkdownMirrorsThePlayer(t *testing.T) {
	got := sceneMarkdown("## The problem\n\nA **bold** `code` *em* line.\n\n- one\n- two\n\n1. first\n\n> quoted")
	require.Equal(t, "<h4>The problem</h4>"+
		"<p>A <strong>bold</strong> <code>code</code> <em>em</em> line.</p>"+
		"<ul><li>one</li><li>two</li></ul>"+
		"<ol><li>first</li></ol>"+
		"<blockquote>quoted</blockquote>", got)
}

// TestCleanScenariosRefusesForeignDirectory: the same interlock cleanOutput
// holds for docs/docs, for the scenarios tree.
func TestCleanScenariosRefusesForeignDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "hand-made")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "index.html"), []byte("<h1>mine</h1>"), 0o644))

	err := cleanScenarios(dir)
	require.ErrorContains(t, err, "refusing to replace")
	require.FileExists(t, filepath.Join(sub, "index.html"), "nothing was deleted")

	require.NoError(t, os.WriteFile(filepath.Join(sub, "index.html"), []byte(generatedMarker+"\n<h1>ours</h1>"), 0o644))
	require.NoError(t, cleanScenarios(dir))
	require.NoDirExists(t, dir)

	require.NoError(t, cleanScenarios(filepath.Join(t.TempDir(), "missing")), "a missing dir is fine")
}
