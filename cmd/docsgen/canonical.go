package main

// siteBaseURL is the canonical origin of the published site. A constant rather
// than a flag: docs/docs/ is committed and docs-check diffs it, so a flag would
// create two byte-different render paths and phantom drift, and the tests call
// loadSite/renderPages/writeSite directly with no flags. This does not touch the
// base-URL-free href invariant (Root/DocsRoot stay relative); only metadata that
// is absolute by definition -- sitemap locations, canonical URLs -- uses it.
const siteBaseURL = "https://thereisnospoon.org"

// docsPathPrefix is where the generated docs tree is served, relative to the
// site root (GitHub Pages serves docs/ verbatim, so docs/docs/ is /docs/).
const docsPathPrefix = "/docs/"

// Canonical returns the page's absolute canonical URL on the published site.
func (p *Page) Canonical() string { return siteBaseURL + docsPathPrefix + p.URL }
