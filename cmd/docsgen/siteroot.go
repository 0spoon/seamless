package main

import (
	"path/filepath"
	"strings"
)

// rootURLs are the site-root pages docsgen does not render but the sitemap must
// still name -- today just the hand-written landing page. Deliberately a
// constant rather than a directory scan, which would sweep in install,
// install.ps1, SECURITY.md, and static assets.
var rootURLs = []string{"/"}

// robotsTxt is written verbatim to the site root. The marker line is
// load-bearing: the live host sits behind Cloudflare, which can serve its own
// managed robots.txt, and grepping for the marker is the only way to tell from
// outside whose file is being served. Content-Signal ai-input=yes opts into
// inference-time retrieval (being fetched and cited by AI answers).
const robotsTxt = `# seamless-robots-v1
User-agent: *
Allow: /
Content-Signal: search=yes, ai-input=yes, ai-train=yes

Sitemap: ` + siteBaseURL + `/sitemap.xml
`

// xmlEscaper covers the characters XML 1.0 cannot carry raw in text content.
// Page URLs are slug-derived and never contain them today; escaping anyway
// keeps a future odd filename from producing a silently invalid sitemap.
var xmlEscaper = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;",
)

// sitemapLocs is every published URL: the site-root pages first, then the docs
// pages in nav order (deterministic by construction).
func sitemapLocs(site *Site) []string {
	locs := make([]string, 0, len(rootURLs)+len(site.Pages))
	for _, u := range rootURLs {
		locs = append(locs, siteBaseURL+u)
	}
	for _, p := range site.Pages {
		locs = append(locs, p.Canonical())
	}
	return locs
}

// sitemapXML renders the sitemap. No lastmod, changefreq, or priority: the
// render has no deterministic date source (see determinism_test.go), an
// identical lastmod across every URL is what crawlers discard as unreliable,
// and at this page count the other two fields change nothing.
func sitemapXML(site *Site) []byte {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">\n")
	for _, loc := range sitemapLocs(site) {
		b.WriteString("  <url><loc>")
		b.WriteString(xmlEscaper.Replace(loc))
		b.WriteString("</loc></url>\n")
	}
	b.WriteString("</urlset>\n")
	return []byte(b.String())
}

// writeSiteRoot writes the crawler files into the site root (docs/, the
// directory GitHub Pages serves). Unlike writeSite it owns only the files it
// names and never deletes anything: the site root also holds the hand-written
// landing page, CNAME, .nojekyll, and fonts, none of which are docsgen's to
// touch.
func writeSiteRoot(siteDir string, site *Site) error {
	if err := writeFile(filepath.Join(siteDir, "sitemap.xml"), sitemapXML(site)); err != nil {
		return err
	}
	return writeFile(filepath.Join(siteDir, "robots.txt"), []byte(robotsTxt))
}
