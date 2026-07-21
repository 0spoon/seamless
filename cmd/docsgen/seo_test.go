package main

import (
	"html/template"
	"image"
	_ "image/png"
	"os"
	"path/filepath"
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
