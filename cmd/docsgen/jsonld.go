package main

import (
	"encoding/json"
	"fmt"
	"html/template"
)

// websiteID is the @id of the WebSite node the hand-written landing page
// (docs/index.html) declares in its @graph. Every generated page's TechArticle
// points its isPartOf here, so the docs and the landing page describe one site
// entity rather than 41 disconnected ones. seo_test pins the two files to the
// same value.
const websiteID = siteBaseURL + "/#website"

// Crumb is one ancestor in a page's breadcrumb trail.
type Crumb struct {
	Title string
	// URL is docs-root-relative, like Page.URL; "" is the docs home.
	URL string
}

// Breadcrumbs returns the trail above the page -- the docs home, then the
// section when it has a landing URL of its own. The page itself is not
// included: the templates render it as the unlinked current item, and JSONLD
// appends it with its canonical URL. The docs home returns nil (a one-item
// trail navigates nowhere), and root-section pages get no section crumb
// because the root section has no landing page to link.
func (p *Page) Breadcrumbs() []Crumb {
	if p.IsHome() {
		return nil
	}
	crumbs := []Crumb{{Title: "Docs", URL: ""}}
	if sec := p.Section; sec != nil && sec.Slug != "" && sec.Index != nil && sec.Index != p {
		crumbs = append(crumbs, Crumb{Title: sec.Title, URL: sec.Index.URL})
	}
	return crumbs
}

// The JSON-LD vocabulary, as structs rather than map[string]any: a map is
// deterministic today only because encoding/json sorts its keys, while a
// struct is deterministic by declaration order and cannot be broken by a
// refactor. No date fields anywhere -- there is no deterministic date source,
// and TestOutputHasNoBuildTimestamp exists to keep it that way.
type ldGraph struct {
	Context string `json:"@context"`
	Graph   []any  `json:"@graph"`
}

type ldRef struct {
	ID string `json:"@id"`
}

type ldListItem struct {
	Type     string `json:"@type"`
	Position int    `json:"position"`
	Name     string `json:"name"`
	Item     string `json:"item"`
}

type ldBreadcrumbList struct {
	Type  string       `json:"@type"`
	Items []ldListItem `json:"itemListElement"`
}

type ldTechArticle struct {
	Type        string `json:"@type"`
	Headline    string `json:"headline"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url"`
	InLanguage  string `json:"inLanguage"`
	IsPartOf    ldRef  `json:"isPartOf"`
}

// JSONLD returns the page's complete <script type="application/ld+json">
// element: a BreadcrumbList mirroring the visible breadcrumb nav plus a
// minimal TechArticle, one block per page. The docs home returns nothing --
// its og:type is website, and the landing page's @graph already carries the
// site-level entities.
//
// The JSON is marshalled in Go and the whole element is returned as
// template.HTML into plain HTML context. Interpolating the JSON inside a
// <script> written in the template would not work: html/template treats
// application/ld+json as a JS context and would re-escape the payload into a
// quoted JS string literal. encoding/json's default HTML escaping (< > & to
// \uXXXX) keeps a literal "</script>" in content from terminating the element.
func (p *Page) JSONLD() (template.HTML, error) {
	if p.IsHome() {
		return "", nil
	}
	var items []ldListItem
	for _, c := range p.Breadcrumbs() {
		items = append(items, ldListItem{
			Type:     "ListItem",
			Position: len(items) + 1,
			Name:     c.Title,
			Item:     siteBaseURL + docsPathPrefix + c.URL,
		})
	}
	items = append(items, ldListItem{
		Type:     "ListItem",
		Position: len(items) + 1,
		Name:     p.Title,
		Item:     p.Canonical(),
	})

	doc := ldGraph{
		Context: "https://schema.org",
		Graph: []any{
			ldBreadcrumbList{Type: "BreadcrumbList", Items: items},
			ldTechArticle{
				Type:        "TechArticle",
				Headline:    p.HeadTitle(),
				Description: p.Description,
				URL:         p.Canonical(),
				InLanguage:  "en",
				IsPartOf:    ldRef{ID: websiteID},
			},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("%s: marshal JSON-LD: %w", p.URL, err)
	}
	return template.HTML(`<script type="application/ld+json">` + string(raw) + `</script>`), nil //nolint:gosec // marshalled from static structs, no user input
}
