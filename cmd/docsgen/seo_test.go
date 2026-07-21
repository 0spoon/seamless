package main

import (
	"encoding/json"
	"html/template"
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHeadTitle: one method feeds <title>, og:title, and any future JSON-LD
// headline, so its two shapes are pinned here.
func TestHeadTitle(t *testing.T) {
	home := &Page{URL: "", Title: "Seamless"}
	require.Equal(t, "Seamless documentation", home.HeadTitle())

	page := &Page{URL: "quickstart/", Title: "Quickstart"}
	require.Equal(t, "Quickstart - Seamless docs", page.HeadTitle())
}

func TestOGType(t *testing.T) {
	require.Equal(t, "website", (&Page{URL: ""}).OGType())
	require.Equal(t, "article", (&Page{URL: "quickstart/"}).OGType())
}

// TestEveryPageHasCanonicalMatchingItsPath: a plain "contains a canonical"
// check misses the classic copy-paste failure where every page carries the same
// canonical, so each page is checked against its own URL.
func TestEveryPageHasCanonicalMatchingItsPath(t *testing.T) {
	repoRoot(t)

	files := renderRepoSite(t)
	site, err := loadSite("docs-src")
	require.NoError(t, err)

	for _, p := range site.Pages {
		body, ok := files[p.Out]
		require.True(t, ok, "%s was rendered", p.URL)
		require.Contains(t, body, `<link rel="canonical" href="`+p.Canonical()+`">`,
			"%s canonical must match its own path", p.URL)
	}
}

// TestEveryPageHasFullSocialSet: all pages ship the complete head metadata --
// robots snippet opt-out, the og:* set, and the twitter card. og:url and
// og:title must equal the canonical and HeadTitle so unfurlers and search see
// one consistent identity per page.
func TestEveryPageHasFullSocialSet(t *testing.T) {
	repoRoot(t)

	files := renderRepoSite(t)
	site, err := loadSite("docs-src")
	require.NoError(t, err)

	for _, p := range site.Pages {
		body := files[p.Out]
		for _, want := range []string{
			`<meta name="robots" content="max-image-preview:large, max-snippet:-1">`,
			`<meta property="og:site_name" content="Seamless">`,
			`<meta property="og:type" content="` + p.OGType() + `">`,
			`<meta property="og:url" content="` + p.Canonical() + `">`,
			`<meta property="og:title" content="` + template.HTMLEscapeString(p.HeadTitle()) + `">`,
			`<meta property="og:image" content="` + p.OGImage() + `">`,
			`<meta property="og:image:width" content="1200">`,
			`<meta property="og:image:height" content="630">`,
			`<meta property="og:image:alt" content="` + template.HTMLEscapeString(ogImageAlt) + `">`,
			`<meta name="twitter:card" content="summary_large_image">`,
		} {
			require.Contains(t, body, want, "%s is missing %s", p.URL, want)
		}
		if p.Description != "" {
			desc := template.HTMLEscapeString(p.Description)
			require.Contains(t, body, `<meta property="og:description" content="`+desc+`">`, "%s", p.URL)
		}
	}
}

// TestNoTwitterDuplicateTags: twitter:title/description/image are deliberately
// omitted -- every major unfurler falls back to og:*, and mirroring tags can
// only drift from the ones they copy.
func TestNoTwitterDuplicateTags(t *testing.T) {
	repoRoot(t)

	for name, body := range renderRepoSite(t) {
		for _, banned := range []string{"twitter:title", "twitter:description", "twitter:image"} {
			require.NotContains(t, body, banned, "%s carries a twitter tag that can only drift from og:*", name)
		}
	}
}

// TestOGImageExistsWithDeclaredDimensions: the template hardcodes 1200x630, so
// the committed image must actually be that size, and it must live where
// OGImage points (docs/ is the served site root).
func TestOGImageExistsWithDeclaredDimensions(t *testing.T) {
	repoRoot(t)

	require.Equal(t, siteBaseURL+"/static/og.png", (&Page{}).OGImage())

	f, err := os.Open(filepath.Join("docs", filepath.FromSlash(strings.TrimPrefix(ogImagePath, "/"))))
	require.NoError(t, err)
	defer f.Close() //nolint:errcheck // read-only open

	cfg, format, err := image.DecodeConfig(f)
	require.NoError(t, err)
	require.Equal(t, "png", format)
	require.Equal(t, 1200, cfg.Width)
	require.Equal(t, 630, cfg.Height)
}

// TestBreadcrumbs pins the trail shapes: the home has none, root-section pages
// get only the docs-home crumb (the root section has no landing URL to link),
// sectioned pages add their section, and a section index does not list itself
// twice.
func TestBreadcrumbs(t *testing.T) {
	root := &Section{Title: "Getting started", Slug: ""}
	sec := &Section{Title: "Concepts", Slug: "concepts"}
	sec.Index = &Page{Section: sec, URL: "concepts/", Title: "Concepts"}

	require.Nil(t, (&Page{Section: root, URL: ""}).Breadcrumbs())
	require.Equal(t, []Crumb{{Title: "Docs", URL: ""}},
		(&Page{Section: root, URL: "quickstart/"}).Breadcrumbs())
	require.Equal(t, []Crumb{{Title: "Docs", URL: ""}, {Title: "Concepts", URL: "concepts/"}},
		(&Page{Section: sec, URL: "concepts/memory/"}).Breadcrumbs())
	require.Equal(t, []Crumb{{Title: "Docs", URL: ""}}, sec.Index.Breadcrumbs())
}

var ldBlockRe = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)

// ldBlocks returns the payload of every JSON-LD script element in a page.
func ldBlocks(body string) []string {
	var out []string
	for _, m := range ldBlockRe.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestJSONLDRoundTripsOnEveryPage: the classic way JSON-LD ships broken is the
// template layer escaping the payload after it was marshalled, so every block
// must survive json.Unmarshal of the exact bytes in the output file. Every
// page except the docs home carries exactly one block; the home carries none
// (the landing page's @graph owns the site-level entities).
func TestJSONLDRoundTripsOnEveryPage(t *testing.T) {
	repoRoot(t)

	files := renderRepoSite(t)
	site, err := loadSite("docs-src")
	require.NoError(t, err)

	for _, p := range site.Pages {
		blocks := ldBlocks(files[p.Out])
		if p.IsHome() {
			require.Empty(t, blocks, "the docs home carries no JSON-LD")
			continue
		}
		require.Len(t, blocks, 1, "%s carries exactly one JSON-LD block", p.URL)
		var doc any
		require.NoError(t, json.Unmarshal([]byte(blocks[0]), &doc),
			"%s JSON-LD does not round-trip; the template layer mangled it", p.URL)
	}
}

// TestJSONLDMatchesPageIdentity: the structured data must describe the page it
// is on -- breadcrumb trail ending at this page's title and canonical, and a
// TechArticle whose headline, url, and isPartOf anchor line up with the head
// metadata and the landing page's WebSite node.
func TestJSONLDMatchesPageIdentity(t *testing.T) {
	repoRoot(t)

	files := renderRepoSite(t)
	site, err := loadSite("docs-src")
	require.NoError(t, err)

	for _, p := range site.Pages {
		if p.IsHome() {
			continue
		}
		var doc struct {
			Context string            `json:"@context"`
			Graph   []json.RawMessage `json:"@graph"`
		}
		require.NoError(t, json.Unmarshal([]byte(ldBlocks(files[p.Out])[0]), &doc), "%s", p.URL)
		require.Equal(t, "https://schema.org", doc.Context, "%s", p.URL)
		require.Len(t, doc.Graph, 2, "%s: one BreadcrumbList and one TechArticle", p.URL)

		var bc ldBreadcrumbList
		require.NoError(t, json.Unmarshal(doc.Graph[0], &bc), "%s", p.URL)
		require.Equal(t, "BreadcrumbList", bc.Type, "%s", p.URL)
		require.Len(t, bc.Items, len(p.Breadcrumbs())+1, "%s: ancestors plus the page itself", p.URL)
		for i, item := range bc.Items {
			require.Equal(t, "ListItem", item.Type, "%s", p.URL)
			require.Equal(t, i+1, item.Position, "%s positions are 1-based and sequential", p.URL)
		}
		require.Equal(t, "Docs", bc.Items[0].Name, "%s trail starts at the docs home", p.URL)
		require.Equal(t, siteBaseURL+docsPathPrefix, bc.Items[0].Item, "%s", p.URL)
		last := bc.Items[len(bc.Items)-1]
		require.Equal(t, p.Title, last.Name, "%s trail ends at the page", p.URL)
		require.Equal(t, p.Canonical(), last.Item, "%s", p.URL)

		var art ldTechArticle
		require.NoError(t, json.Unmarshal(doc.Graph[1], &art), "%s", p.URL)
		require.Equal(t, "TechArticle", art.Type, "%s", p.URL)
		require.Equal(t, p.HeadTitle(), art.Headline, "%s headline shares HeadTitle with <title> and og:title", p.URL)
		require.Equal(t, p.Description, art.Description, "%s", p.URL)
		require.Equal(t, p.Canonical(), art.URL, "%s", p.URL)
		require.Equal(t, "en", art.InLanguage, "%s", p.URL)
		require.Equal(t, websiteID, art.IsPartOf.ID, "%s anchors to the landing page's WebSite node", p.URL)
	}
}

// TestVisibleBreadcrumbMatchesJSONLD: structured data should describe visible
// content, so every page with a BreadcrumbList must render the same trail as a
// real <nav> -- linked ancestors, then the page as the unlinked current item.
func TestVisibleBreadcrumbMatchesJSONLD(t *testing.T) {
	repoRoot(t)

	files := renderRepoSite(t)
	site, err := loadSite("docs-src")
	require.NoError(t, err)

	for _, p := range site.Pages {
		body := files[p.Out]
		if p.IsHome() {
			require.NotContains(t, body, `class="breadcrumb"`, "the docs home renders no breadcrumb")
			continue
		}
		require.Contains(t, body, `<nav class="breadcrumb" aria-label="Breadcrumb">`, "%s", p.URL)
		for _, c := range p.Breadcrumbs() {
			require.Contains(t, body,
				`<a href="`+p.DocsRoot+c.URL+`">`+template.HTMLEscapeString(c.Title)+`</a>`,
				"%s links its %q crumb", p.URL, c.Title)
		}
		require.Contains(t, body,
			`<li aria-current="page">`+template.HTMLEscapeString(p.Title)+`</li>`,
			"%s renders itself as the current item", p.URL)
	}
}

// TestOutputHasNoDateFields is the schema-shaped half of the no-timestamp rule:
// there is no deterministic date source, so no output may carry the JSON-LD or
// sitemap date vocabulary. TestOutputHasNoBuildTimestamp catches literal dates;
// this catches the field names that would hold one.
func TestOutputHasNoDateFields(t *testing.T) {
	repoRoot(t)

	for name, body := range renderRepoSite(t) {
		for _, field := range []string{"datePublished", "dateModified", "lastmod"} {
			require.NotContains(t, body, field, "%s", name)
		}
	}
}

// TestLandingPageDeclaresTheWebsiteNode: every generated page's TechArticle
// points isPartOf at websiteID, an @id only the hand-written landing page
// defines. If the landing page's @graph loses that node (or changes its @id),
// 40 pages point at nothing.
func TestLandingPageDeclaresTheWebsiteNode(t *testing.T) {
	repoRoot(t)

	raw, err := os.ReadFile(filepath.Join("docs", "index.html"))
	require.NoError(t, err)
	landing := string(raw)
	require.Contains(t, landing, `"@type": "WebSite"`)
	require.Contains(t, landing, `"@id": "`+websiteID+`"`)
}
