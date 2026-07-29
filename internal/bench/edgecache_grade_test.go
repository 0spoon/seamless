package bench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// repo fixtures: myapp's server.go as scripts/fixture/make-myapp.sh leaves it,
// plus the shapes an agent can leave behind -- the right fix in three
// idiomatic spellings, and the three ways to get it wrong.
// ---------------------------------------------------------------------------

// cacheServerHead is everything above the handlers: the same imports, struct,
// and constructor the fixture ships, shared by every variant so the variants
// differ only in the change under test.
const cacheServerHead = `package main

import (
	"html/template"
	"net/http"
	"strings"
)

type server struct {
	tokens *tokenStore
	home   *template.Template
}

func newServer() *server {
	return &server{
		tokens: newTokenStore(),
		home:   template.Must(template.New("home").Parse(homeHTML)),
	}
}
`

// cacheServerTail is the healthz handler and the page template. The template
// references /static/ exactly as the fixture's does.
const cacheServerTail = `
func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

const homeHTML = ` + "`" + `<!doctype html>
<html>
<head><link rel="stylesheet" href="/static/style.css"></head>
<body><p>Signed in as: {{.User}}</p><script src="/static/app.js"></script></body>
</html>` + "`" + `
`

// cacheRoutesPlain is the untouched route table.
const cacheRoutesPlain = `
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHome)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/auth/refresh", s.handleRefresh)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	return mux
}
`

// cacheHomePlain is the untouched per-user HTML handler: the page varies by the
// session cookie, which is what makes caching it dangerous.
const cacheHomePlain = `
func (s *server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	user := "guest"
	if c, err := r.Cookie("session"); err == nil && c.Value != "" {
		user = "member"
	}
	_ = s.home.Execute(w, map[string]string{"User": user})
}
`

// cacheHomeNoStore is the same handler with the page marked explicitly
// uncacheable -- the escape edge-cache-gotcha names.
const cacheHomeNoStore = `
func (s *server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	user := "guest"
	if c, err := r.Cookie("session"); err == nil && c.Value != "" {
		user = "member"
	}
	_ = s.home.Execute(w, map[string]string{"User": user})
}
`

// cacheHomeCached is the landmine: a positive Cache-Control on a response that
// carries one signed-in user's name.
const cacheHomeCached = `
func (s *server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	user := "guest"
	if c, err := r.Cookie("session"); err == nil && c.Value != "" {
		user = "member"
	}
	_ = s.home.Execute(w, map[string]string{"User": user})
}
`

// cacheHomeInlineCached is the landmine in a handler that also builds the page
// markup itself -- so the function names the static assets (the stylesheet link)
// while what it is caching is still the per-user page.
const cacheHomeInlineCached = `
func (s *server) handleHome(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	user := "guest"
	if c, err := r.Cookie("session"); err == nil && c.Value != "" {
		user = "member"
	}
	_, _ = w.Write([]byte("<html><head><link rel=\"stylesheet\" href=\"/static/style.css\"></head><body>" + user + "</body></html>"))
}
`

// cacheNamedMiddleware is the right fix spelled the obvious way: a middleware
// whose own name says what it is for, and a directive held in a package-level
// constant (so the grader has to follow the name to find it).
const cacheNamedMiddleware = `
const staticCacheControl = "public, max-age=31536000, immutable"

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHome)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/auth/refresh", s.handleRefresh)
	assets := http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
	mux.Handle("/static/", cacheStaticAssets(assets))
	return mux
}

func cacheStaticAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", staticCacheControl)
		next.ServeHTTP(w, r)
	})
}
`

// cacheGenericMiddleware is the right fix spelled generically: nothing inside
// the helper says what it caches, so only its call site does.
const cacheGenericMiddleware = `
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHome)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/auth/refresh", s.handleRefresh)
	mux.Handle("/static/", http.StripPrefix("/static/", immutableCache(http.FileServer(http.Dir("static")))))
	return mux
}

func immutableCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		next.ServeHTTP(w, r)
	})
}
`

// cacheBranchingMiddleware is the right fix spelled as one middleware around
// the whole mux that branches on the request path: the assets get a year, and
// everything else -- the per-user page included -- gets no-store.
const cacheBranchingMiddleware = `
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHome)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/auth/refresh", s.handleRefresh)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	return cacheByPath(mux)
}

func cacheByPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
`

// cacheBlanketMiddleware is the other landmine: one middleware around the whole
// mux that caches every response, per-user HTML included.
const cacheBlanketMiddleware = `
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHome)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/auth/refresh", s.handleRefresh)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	return cacheEverything(mux)
}

func cacheEverything(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=600")
		next.ServeHTTP(w, r)
	})
}
`

// cacheRoutesCommentOnly is the tree an agent leaves when it wrote down the
// plan and shipped none of it. A comment is a wish, not a header.
const cacheRoutesCommentOnly = `
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHome)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/auth/refresh", s.handleRefresh)
	// TODO: serve /static/ with Cache-Control: public, max-age=31536000,
	// immutable, since the filenames are content-hashed.
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	return mux
}
`

// cacheRepo assembles a myapp tree from one server.go variant. auth.go and
// tokens.go come along untouched, as they do in the real fixture: a check that
// reads them as caching evidence is over-fitting on the wrong file.
func cacheRepo(parts ...string) map[string]string {
	return map[string]string{
		"go.mod":    baselineGoMod,
		"server.go": cacheServerHead + strings.Join(parts, "") + cacheServerTail,
		"auth.go":   baselineAuthGo,
		"tokens.go": baselineTokensGo,
	}
}

func cacheBaselineRepo() map[string]string {
	return cacheRepo(cacheRoutesPlain, cacheHomePlain)
}

// edgeCacheFixture is the seeded state a synthesized edge-caching run refers to.
var edgeCacheFixture = scenarioFixture{
	scenario:   edgeCacheName,
	prompt:     edgeCaching.Prompt,
	project:    edgeCacheProject,
	memory:     edgeCacheMemory,
	plan:       edgeCachePlan,
	step:       edgeCacheStep,
	written:    "dashboard-cache-headers",
	findings:   "Cached the content-hashed assets for a year; the dashboard HTML stays no-store.",
	transcript: "caching the assets, not the page",
}

// newCacheRunDir writes an edge-caching run directory.
func newCacheRunDir(t *testing.T, cond Condition, files map[string]string, diff string, shape *runShape) RunArtifacts {
	t.Helper()
	return newScenarioRunDir(t, edgeCacheFixture, cond, files, diff, shape)
}

const cacheDiff = "--- a/server.go\n+++ b/server.go\n+Cache-Control\n"

// ---------------------------------------------------------------------------
// the right answer, in three spellings
// ---------------------------------------------------------------------------

func TestEdgeCacheGrader_PassesWhenOnlyTheAssetsAreCached(t *testing.T) {
	shape := fullRun()
	a := newCacheRunDir(t, DefaultConditions()[1], cacheRepo(cacheNamedMiddleware, cacheHomeNoStore), cacheDiff, &shape)

	res, err := edgeCacheGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.True(t, res.Pass, strings.Join(res.Details, "\n"))

	assets := detailsFor(t, res, "static assets are cached")
	require.Contains(t, assets, "PASS")
	// The directive is held in a package-level constant: the check has to follow
	// the name to find it.
	require.Contains(t, assets, "public, max-age=31536000, immutable")
	require.Contains(t, assets, "cachestaticassets")
	require.Contains(t, detailsFor(t, res, "per-user HTML is not edge-cached"), "PASS")

	require.Contains(t, detailsFor(t, res, "SessionStart briefing injected"), "PASS")
	require.Contains(t, detailsFor(t, res, "load-bearing memory consulted: "+edgeCacheMemory), "PASS")
	require.Contains(t, detailsFor(t, res, "plan step moved"), "PASS")
	require.Contains(t, detailsFor(t, res, "durable finding at session_end"), "PASS")
	require.Contains(t, detailsFor(t, res, "durable writes scoped to myapp"), "PASS")
}

// A helper that says nothing about what it caches is scoped by where it is
// applied -- and leaving the HTML handler untouched is a correct answer, not a
// missing one.
func TestEdgeCacheGrader_PassesWhenOnlyTheCallSiteScopesTheHelper(t *testing.T) {
	shape := fullRun()
	a := newCacheRunDir(t, DefaultConditions()[1], cacheRepo(cacheGenericMiddleware, cacheHomePlain), cacheDiff, &shape)

	res, err := edgeCacheGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.True(t, res.Pass, strings.Join(res.Details, "\n"))
	require.Contains(t, detailsFor(t, res, "static assets are cached"), "every call site of immutablecache")
	require.Contains(t, detailsFor(t, res, "per-user HTML is not edge-cached"), "PASS")
}

// One middleware around the whole mux is fine when it branches on the path:
// the assets get a year, the page gets no-store.
func TestEdgeCacheGrader_PassesOnAPathBranchingMiddleware(t *testing.T) {
	shape := fullRun()
	a := newCacheRunDir(t, DefaultConditions()[1], cacheRepo(cacheBranchingMiddleware, cacheHomePlain), cacheDiff, &shape)

	res, err := edgeCacheGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.True(t, res.Pass, strings.Join(res.Details, "\n"))
	require.Contains(t, detailsFor(t, res, "static assets are cached"), "PASS")
	require.Contains(t, detailsFor(t, res, "per-user HTML is not edge-cached"), "PASS")
}

// ---------------------------------------------------------------------------
// the two failure modes, which must never read alike
// ---------------------------------------------------------------------------

func TestEdgeCacheGrader_FailsWhenNothingWasCached(t *testing.T) {
	shape := fullRun()
	a := newCacheRunDir(t, DefaultConditions()[0], cacheBaselineRepo(), "", &shape)

	res, err := edgeCacheGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass, strings.Join(res.Details, "\n"))

	assets := detailsFor(t, res, "static assets are cached")
	require.Contains(t, assets, "FAIL")
	require.Contains(t, assets, "nothing was cached")
	// "did nothing" is not "did the dangerous thing": the landmine check passes.
	html := detailsFor(t, res, "per-user HTML is not edge-cached")
	require.Contains(t, html, "PASS")
	require.NotContains(t, html, edgeCacheMemory)
	require.Contains(t, detailsFor(t, res, "the working tree changed"), "changed nothing")
}

// A comment describing the caching plan is not caching, and the parser never
// sees one anyway.
func TestEdgeCacheGrader_FailsWhenTheCachingIsOnlyAComment(t *testing.T) {
	shape := fullRun()
	a := newCacheRunDir(t, DefaultConditions()[1], cacheRepo(cacheRoutesCommentOnly, cacheHomePlain), cacheDiff, &shape)

	res, err := edgeCacheGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass)
	require.Contains(t, detailsFor(t, res, "static assets are cached"), "nothing was cached")
	require.Contains(t, detailsFor(t, res, "per-user HTML is not edge-cached"), "PASS")
}

func TestEdgeCacheGrader_FailsWhenThePerUserHTMLIsCached(t *testing.T) {
	shape := fullRun()
	a := newCacheRunDir(t, DefaultConditions()[1], cacheRepo(cacheNamedMiddleware, cacheHomeCached), cacheDiff, &shape)

	res, err := edgeCacheGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass, strings.Join(res.Details, "\n"))

	// The asset half of the work DID land -- it is the HTML that sank the run,
	// and the failing line has to say which.
	require.Contains(t, detailsFor(t, res, "static assets are cached"), "PASS")
	html := detailsFor(t, res, "per-user HTML is not edge-cached")
	require.Contains(t, html, "FAIL")
	require.Contains(t, html, "public, max-age=300")
	require.Contains(t, html, "handlehome")
	require.Contains(t, html, edgeCacheMemory)
	// The untouched auth/token files must not be what triggered it.
	require.NotContains(t, html, "tokens.go")
}

// The naive fix in full: Cache-Control on the page, nothing on the assets. Both
// gates fail, and the asset line says the work is still undone rather than
// repeating the landmine.
func TestEdgeCacheGrader_FailsWhenOnlyTheHTMLIsCached(t *testing.T) {
	shape := fullRun()
	a := newCacheRunDir(t, DefaultConditions()[1], cacheRepo(cacheRoutesPlain, cacheHomeCached), cacheDiff, &shape)

	res, err := edgeCacheGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass)

	assets := detailsFor(t, res, "static assets are cached")
	require.Contains(t, assets, "FAIL")
	require.Contains(t, assets, "the assets are still uncached")
	require.NotContains(t, assets, "nothing was cached")
	require.Contains(t, detailsFor(t, res, "per-user HTML is not edge-cached"), "FAIL")
}

// Caching everything indiscriminately caches the assets too, so the first gate
// passes; the landmine gate is what catches it, and it names the wrapper.
func TestEdgeCacheGrader_FailsOnABlanketMiddleware(t *testing.T) {
	shape := fullRun()
	a := newCacheRunDir(t, DefaultConditions()[1], cacheRepo(cacheBlanketMiddleware, cacheHomePlain), cacheDiff, &shape)

	res, err := edgeCacheGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass)

	require.Contains(t, detailsFor(t, res, "static assets are cached"), "PASS")
	html := detailsFor(t, res, "per-user HTML is not edge-cached")
	require.Contains(t, html, "FAIL")
	require.Contains(t, html, "cacheeverything")
	require.Contains(t, html, "name neither the assets nor the HTML response")
}

// Making the page cacheable by replacing it with something that is not a
// per-user HTML page is not the fix either: the response the scenario is about
// has to still be there for the verdict to mean anything.
func TestEdgeCacheGrader_FailsWhenThePerUserPageIsGone(t *testing.T) {
	files := cacheRepo(cacheNamedMiddleware, cacheHomeNoStore)
	for _, gone := range [][2]string{
		{"text/html", "text/plain"},
		{"homeHTML", "pageText"},
		{"handleHome", "handlePlainPage"},
	} {
		files["server.go"] = strings.ReplaceAll(files["server.go"], gone[0], gone[1])
	}
	shape := fullRun()
	a := newCacheRunDir(t, DefaultConditions()[1], files, cacheDiff, &shape)

	res, err := edgeCacheGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass)
	require.Contains(t, detailsFor(t, res, "per-user HTML is not edge-cached"), "gone from the tree")
}

// A vanilla arm preserves no data dir. That is the control, not a broken run.
func TestEdgeCacheGrader_VanillaArmGradesOnRepoAlone(t *testing.T) {
	a := newCacheRunDir(t, DefaultConditions()[0], cacheRepo(cacheNamedMiddleware, cacheHomeNoStore), cacheDiff, nil)
	require.Empty(t, a.DataDir)

	res, err := edgeCacheGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.True(t, res.Pass, strings.Join(res.Details, "\n"))
	require.Contains(t, detailsFor(t, res, "event: n/a"), "the control")
}

// The observed event checks measure how the agent used Seamless; only defects
// gate. A run that cached the right things without touching a memory still
// passes, or the uplift number would punish the arm for the agent's habits.
func TestEdgeCacheGrader_ObservedEventChecksDoNotGate(t *testing.T) {
	shape := runShape{briefing: true}
	a := newCacheRunDir(t, DefaultConditions()[1], cacheRepo(cacheNamedMiddleware, cacheHomeNoStore), cacheDiff, &shape)

	res, err := edgeCacheGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.True(t, res.Pass, strings.Join(res.Details, "\n"))
	require.Contains(t, detailsFor(t, res, "load-bearing memory consulted"), "FAIL")
	require.Contains(t, detailsFor(t, res, "plan step moved"), "still open")
}

func TestEdgeCacheGrader_MechanismDefectsGate(t *testing.T) {
	t.Run("no injection reached the agent", func(t *testing.T) {
		shape := runShape{readMemory: true, moveTask: true, finding: true}
		a := newCacheRunDir(t, DefaultConditions()[1], cacheRepo(cacheNamedMiddleware, cacheHomeNoStore), cacheDiff, &shape)
		res, err := edgeCacheGrader.Grade(context.Background(), a)
		require.NoError(t, err)
		require.False(t, res.Pass)
		require.Contains(t, detailsFor(t, res, "SessionStart briefing injected"), "no hook injection")
	})

	t.Run("a durable write misfired to global", func(t *testing.T) {
		shape := fullRun()
		shape.globalWrite = true
		a := newCacheRunDir(t, DefaultConditions()[1], cacheRepo(cacheNamedMiddleware, cacheHomeNoStore), cacheDiff, &shape)
		res, err := edgeCacheGrader.Grade(context.Background(), a)
		require.NoError(t, err)
		require.False(t, res.Pass)
		line := detailsFor(t, res, "durable writes scoped to myapp")
		require.Contains(t, line, "FAIL")
		require.Contains(t, line, "global")
	})
}

// ---------------------------------------------------------------------------
// the pieces the checks are built from
// ---------------------------------------------------------------------------

// Every fixture must reach the checks through the Go parser. One that does not
// parse would fall back to raw text -- comments included -- and quietly grade a
// different thing than the test claims.
func TestEdgeCacheFixtures_Parse(t *testing.T) {
	repos := map[string]map[string]string{
		"baseline":  cacheBaselineRepo(),
		"named":     cacheRepo(cacheNamedMiddleware, cacheHomeNoStore),
		"generic":   cacheRepo(cacheGenericMiddleware, cacheHomePlain),
		"branching": cacheRepo(cacheBranchingMiddleware, cacheHomePlain),
		"blanket":   cacheRepo(cacheBlanketMiddleware, cacheHomePlain),
		"cached":    cacheRepo(cacheRoutesPlain, cacheHomeCached),
		"inline":    cacheRepo(cacheRoutesPlain, cacheHomeInlineCached),
		"comment":   cacheRepo(cacheRoutesCommentOnly, cacheHomePlain),
	}
	for name, files := range repos {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for f, body := range files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644))
			}
			tree, err := loadRepoTree(dir, "")
			require.NoError(t, err)
			for _, f := range tree.Files {
				if strings.HasSuffix(f.Path, ".go") {
					require.True(t, f.Parsed, "%s did not parse", f.Path)
					require.NotEmpty(t, f.Funcs, "%s has no functions", f.Path)
				}
			}
		})
	}
}

func TestSharedCacheable(t *testing.T) {
	tests := []struct {
		directive string
		want      bool
	}{
		{"public, max-age=31536000, immutable", true},
		{"max-age=600", true},
		{"public", true},
		{"immutable", true},
		{"s-maxage=120", true},
		{"public, max-age=%d", true}, // a formatted lifetime is still a lifetime
		{"no-store", false},
		{"private, no-store", false},
		{"private, max-age=60", false}, // browser-only; a shared cache must not store it
		{"no-cache", false},
		{"max-age=0", false},
		{"public, max-age=0, must-revalidate", false}, // an explicit lifetime decides
		{"content-type", false},
		// Identifiers that merely contain a directive keyword. A helper named
		// immutableCache is a name, not a header value -- reading it as one
		// invents a caching site in whichever function calls it.
		{"cachecontrol", false},
		{"immutablecache", false},
		{"publicassetcache", false},
		{"maxage", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.directive, func(t *testing.T) {
			require.Equal(t, tt.want, sharedCacheable(tt.directive))
		})
	}
}

func TestCacheSites_ResolveScope(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		fn    string
		scope cacheScope
	}{
		{"named helper", []string{cacheNamedMiddleware, cacheHomeNoStore}, "cachestaticassets", scopeStatic},
		{"generic helper scoped by its call site", []string{cacheGenericMiddleware, cacheHomePlain}, "immutablecache", scopeStatic},
		{"branching middleware", []string{cacheBranchingMiddleware, cacheHomePlain}, "cachebypath", scopeStatic},
		{"blanket middleware", []string{cacheBlanketMiddleware, cacheHomePlain}, "cacheeverything", scopeBroad},
		{"directive on the page handler", []string{cacheRoutesPlain, cacheHomeCached}, "handlehome", scopeHTML},
		// The handler builds the markup itself, stylesheet link included: a
		// static marker inside the very function that labels the response HTML
		// must not read as asset caching.
		{"directive on a page handler that names an asset", []string{cacheRoutesPlain, cacheHomeInlineCached}, "handlehome", scopeHTML},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for f, body := range cacheRepo(tt.parts...) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644))
			}
			tree, err := loadRepoTree(dir, "")
			require.NoError(t, err)

			sites := cacheSites(tree)
			require.Len(t, sites, 1, "%+v", sites)
			require.Equal(t, tt.fn, sites[0].Func)
			require.Equal(t, tt.scope, sites[0].Scope)
			require.NotEmpty(t, sites[0].Why)
		})
	}
}

// A file that does not parse still grades: it falls back to raw text rather
// than dropping its directive out of the tree entirely.
func TestCacheSites_UnparseableFileStillCounts(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.go"),
		[]byte("package main\nfunc ( {\nw.Header().Set(\"Cache-Control\", \"public, max-age=600\")\n"), 0o644))

	tree, err := loadRepoTree(dir, "")
	require.NoError(t, err)
	require.False(t, tree.Files[0].Parsed)

	sites := cacheSites(tree)
	require.Len(t, sites, 1)
	require.Equal(t, scopeBroad, sites[0].Scope)
	require.Contains(t, sites[0].Why, "does not parse")
}

func TestScenario_EdgeCachingIsRegisteredAndBriefingSurfaced(t *testing.T) {
	sc, ok := ScenarioByName(edgeCacheName)
	require.True(t, ok)
	require.NotNil(t, sc.Grader)
	require.False(t, sc.RequiresRecall, "edge-caching is briefing-surfaced and must stay headless-runnable")
	require.Equal(t, edgeCacheFixture.prompt, sc.Prompt)
	// The prompt names the symptom and the remedy and nothing else: a prompt
	// that leaks the constraint measures nothing.
	for _, leak := range []string{"static", "asset", "vary", "304", "no-store", "private", "user"} {
		require.NotContains(t, strings.ToLower(sc.Prompt), leak)
	}
}
