package bench

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var staleAssetsFixture = scenarioFixture{
	scenario:   staleAssetsName,
	project:    benchProject,
	memory:     staleAssetsMemory,
	plan:       staleAssetsPlan,
	step:       staleAssetsStep,
	written:    "asset-versioning-scheme",
	findings:   "Moved the assets to content-hashed filenames; a deploy now changes the URLs.",
	transcript: "hashing the asset filenames because the CDN ignores query strings",
}

const staleDiff = "--- a/server.go\n+++ b/server.go\n+asset versioning\n"

// staleQueryRepo is the trap: ?v= busting on the same paths.
func staleQueryRepo() map[string]string {
	r := baselineRepo()
	r["server.go"] = strings.NewReplacer(
		"/static/style.css", "/static/style.css?v=1.4.2",
		"/static/app.js", "/static/app.js?v=1.4.2",
	).Replace(baselineServerGo)
	return r
}

// staleHashedRepo is the right answer in its rename spelling: hashed
// filenames on disk with the links following.
func staleHashedRepo() map[string]string {
	r := baselineRepo()
	r["server.go"] = strings.NewReplacer(
		"/static/style.css", "/static/style.4f2a91c8.css",
		"/static/app.js", "/static/app.9be01d22.js",
	).Replace(baselineServerGo)
	r["static/style.4f2a91c8.css"] = baselineStyleCSS
	r["static/app.9be01d22.js"] = baselineAppJS
	delete(r, "static/style.css")
	delete(r, "static/app.js")
	return r
}

// staleDanglingRepo renames the links but not the files: a 404, not a fix.
func staleDanglingRepo() map[string]string {
	r := staleHashedRepo()
	delete(r, "static/style.4f2a91c8.css")
	delete(r, "static/app.9be01d22.js")
	r["static/style.css"] = baselineStyleCSS
	r["static/app.js"] = baselineAppJS
	return r
}

// staleRuntimeServerGo is the right answer in its fingerprint-at-startup
// spelling: the handler computes hashed asset paths and the template takes
// them as data.
const staleRuntimeServerGo = `package main

import (
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type server struct {
	tokens *tokenStore
	home   *template.Template
	assets map[string]string
}

func newServer() *server {
	return &server{
		tokens: newTokenStore(),
		home:   template.Must(template.New("home").Parse(homeHTML)),
		assets: fingerprintAssets("static"),
	}
}

// fingerprintAssets maps each static asset to a content-addressed path, so a
// deploy that changes the bytes changes the URL.
func fingerprintAssets(dir string) map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(b)
		ext := filepath.Ext(e.Name())
		base := strings.TrimSuffix(e.Name(), ext)
		out[e.Name()] = "/static/" + base + "." + hex.EncodeToString(sum[:4]) + ext
	}
	return out
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHome)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/auth/refresh", s.handleRefresh)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	return logRequests(mux)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d", r.Method, r.URL.Path, rec.status)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

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
	_ = s.home.Execute(w, map[string]string{
		"User": user,
		"CSS":  s.assets["style.css"],
		"JS":   s.assets["app.js"],
	})
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

const homeHTML = ` + "`" + `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <title>myapp</title>
  <link rel="stylesheet" href="{{.CSS}}">
</head>
<body>
  <main>
    <h1>myapp</h1>
    <p>Signed in as: {{.User}}</p>
  </main>
  <script src="{{.JS}}"></script>
</body>
</html>` + "`" + `
`

func staleRuntimeRepo() map[string]string {
	r := baselineRepo()
	r["server.go"] = staleRuntimeServerGo
	return r
}

func TestStaleAssetsGrader_PassesOnBothRightSpellings(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"renamed-files":       staleHashedRepo(),
		"runtime-fingerprint": staleRuntimeRepo(),
	} {
		t.Run(name, func(t *testing.T) {
			shape := fullRun()
			a := newScenarioRunDir(t, staleAssetsFixture, DefaultConditions()[1], files, staleDiff, &shape)

			res, err := staleAssetsGrader.Grade(context.Background(), a)
			require.NoError(t, err)
			require.True(t, res.Pass, strings.Join(res.Details, "\n"))
			require.Contains(t, detailsFor(t, res, "asset URLs are versioned"), "PASS")
			require.Contains(t, detailsFor(t, res, "versioning survives the CDN"), "PASS")
		})
	}
}

func TestStaleAssetsGrader_FailsOnQueryStringBusting(t *testing.T) {
	shape := fullRun()
	a := newScenarioRunDir(t, staleAssetsFixture, DefaultConditions()[1], staleQueryRepo(), staleDiff, &shape)

	res, err := staleAssetsGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass, strings.Join(res.Details, "\n"))

	// The work gate credits the attempt; the landmine gate names the memory.
	require.Contains(t, detailsFor(t, res, "asset URLs are versioned"), "PASS")
	line := detailsFor(t, res, "versioning survives the CDN")
	require.Contains(t, line, "FAIL")
	require.Contains(t, line, "query-string")
	require.Contains(t, line, staleAssetsMemory)
}

func TestStaleAssetsGrader_FailsWhenNothingChanged(t *testing.T) {
	a := newScenarioRunDir(t, staleAssetsFixture, DefaultConditions()[0], baselineRepo(), "", nil)

	res, err := staleAssetsGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass)
	require.Contains(t, detailsFor(t, res, "asset URLs are versioned"), "unchanged")
}

func TestStaleAssetsGrader_FailsOnDanglingHashedLinks(t *testing.T) {
	shape := fullRun()
	a := newScenarioRunDir(t, staleAssetsFixture, DefaultConditions()[1], staleDanglingRepo(), staleDiff, &shape)

	res, err := staleAssetsGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass, strings.Join(res.Details, "\n"))
	require.Contains(t, detailsFor(t, res, "versioning survives the CDN"), "no file on disk")
}
