package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSitemapCoversEveryPage: the sitemap must name the landing page plus every
// docs page -- and nothing else, so a page dropped from the nav leaves the
// sitemap in the same regeneration that removes its HTML.
func TestSitemapCoversEveryPage(t *testing.T) {
	repoRoot(t)

	site, err := loadSite("docs-src")
	require.NoError(t, err)

	sitemap := string(sitemapXML(site))
	require.Equal(t, len(rootURLs)+len(site.Pages), strings.Count(sitemap, "<loc>"),
		"one <loc> per site-root URL and per docs page")
	require.Contains(t, sitemap, "<loc>"+siteBaseURL+"/</loc>", "the landing page is listed")
	for _, p := range site.Pages {
		require.Contains(t, sitemap, "<loc>"+p.Canonical()+"</loc>", "%s is listed", p.URL)
	}
}

// TestSitemapHasNoDateFields: there is no deterministic date source (see
// determinism_test.go), and a lastmod identical across every URL is what
// crawlers discard as unreliable. Absent beats fake.
func TestSitemapHasNoDateFields(t *testing.T) {
	repoRoot(t)

	site, err := loadSite("docs-src")
	require.NoError(t, err)

	sitemap := string(sitemapXML(site))
	for _, field := range []string{"lastmod", "changefreq", "priority"} {
		require.NotContains(t, sitemap, field)
	}
}

// TestRobotsTxtPointsAtSitemap: the marker line is how anyone can tell whether
// the live host serves this file or Cloudflare's managed one, and the Sitemap
// directive is how crawlers that never see Search Console find the sitemap.
func TestRobotsTxtPointsAtSitemap(t *testing.T) {
	for _, want := range []string{
		"# seamless-robots-v1",
		"User-agent: *",
		"Allow: /",
		"Content-Signal: search=yes, ai-input=yes, ai-train=yes",
		"Sitemap: " + siteBaseURL + "/sitemap.xml",
	} {
		require.Contains(t, robotsTxt, want+"\n")
	}
}

// TestCanonicalHostMatchesCNAME: siteBaseURL is a constant and docs/CNAME is
// what GitHub Pages actually serves; if they diverge, every sitemap URL points
// at a host the site no longer lives on.
func TestCanonicalHostMatchesCNAME(t *testing.T) {
	repoRoot(t)

	raw, err := os.ReadFile(filepath.Join("docs", "CNAME"))
	require.NoError(t, err)
	require.Equal(t, "https://"+strings.TrimSpace(string(raw)), siteBaseURL)
}

// TestWriteSiteRootNeverDeletes: the site root holds the hand-written landing
// page, CNAME, and fonts. writeSiteRoot must only add its own files, never
// clean the directory the way writeSite does.
func TestWriteSiteRootNeverDeletes(t *testing.T) {
	dir := t.TempDir()
	landing := filepath.Join(dir, "index.html")
	cname := filepath.Join(dir, "CNAME")
	require.NoError(t, os.WriteFile(landing, []byte("<h1>hand-written landing page</h1>"), 0o644))
	require.NoError(t, os.WriteFile(cname, []byte("thereisnospoon.org"), 0o644))

	site := &Site{Pages: []*Page{{URL: "quickstart/"}}}
	require.NoError(t, writeSiteRoot(dir, site))

	require.FileExists(t, landing, "the landing page survives")
	require.FileExists(t, cname, "CNAME survives")
	require.FileExists(t, filepath.Join(dir, "sitemap.xml"))
	require.FileExists(t, filepath.Join(dir, "robots.txt"))
}

// TestPageCanonical: canonical URLs are absolute, directory-style, and rooted
// under the published /docs/ prefix.
func TestPageCanonical(t *testing.T) {
	tests := []struct{ url, want string }{
		{"", "https://thereisnospoon.org/docs/"},
		{"quickstart/", "https://thereisnospoon.org/docs/quickstart/"},
		{"reference/mcp/tasks/", "https://thereisnospoon.org/docs/reference/mcp/tasks/"},
	}
	for _, tt := range tests {
		p := &Page{URL: tt.url}
		require.Equal(t, tt.want, p.Canonical())
	}
}
