package main

import (
	"regexp"
	"strings"
)

// This file emits each docs page's markdown twin: an index.md written next to
// the page's index.html, holding the same untruncated source markdown that
// llms-full.txt aggregates -- split back out per page so the CDN can serve it
// for `Accept: text/markdown` content negotiation (see llmstxt.org and
// Cloudflare's "Markdown for Agents"). The negotiation itself lives at the
// edge: a Transform Rule rewrites markdown-accepting requests for /docs/...
// directory URLs to their index.md and stamps Content-Type: text/markdown.
// HTML stays the default for every request that does not ask.

// rootLinkRE matches the destination of an inline markdown link or image whose
// path is root-absolute. Authored destinations never contain spaces or nested
// parentheses (checkLinks resolves every one of them), so the simple form is
// exact for this tree.
var rootLinkRE = regexp.MustCompile(`\]\((/[^)\s]+)\)`)

// markdownTwin renders a page's text/markdown representation: title,
// description blockquote, then the full source markdown -- the same per-page
// shape llms-full.txt uses. Section indexes append their card grid as a link
// list, mirroring what their HTML shows (generated ones have no body at all).
func markdownTwin(p *Page) []byte {
	var b strings.Builder
	b.WriteString("# " + p.Title + "\n")
	if p.Description != "" {
		b.WriteString("\n> " + p.Description + "\n")
	}
	if body := strings.TrimSpace(p.FullMarkdown); body != "" {
		b.WriteString("\n" + absolutizeRootLinks(body) + "\n")
	}
	if p.IsSectionIndex() {
		b.WriteString("\n")
		for _, q := range p.Section.Pages {
			b.WriteString("- [" + q.Title + "](" + q.Canonical() + "): " + q.Description + "\n")
		}
	}
	return []byte(b.String())
}

// absolutizeRootLinks rewrites root-absolute link destinations to canonical
// URLs. The twin is served under its page's directory URL, where a relative
// resolution of `/concepts/memory/` lands at the site root instead of the docs
// root; absolute URLs are the only form every client resolves the same way.
// The path families mirror rewriteDocLinks: /scenarios/ pages live at the site
// root, every other root-absolute path is docs-root-relative.
func absolutizeRootLinks(md string) string {
	return rootLinkRE.ReplaceAllStringFunc(md, func(m string) string {
		p := m[2 : len(m)-1]
		if strings.HasPrefix(p, "//") {
			return m // scheme-relative, not same-site
		}
		if strings.HasPrefix(p, "/"+scenariosDirName+"/") {
			return "](" + siteBaseURL + p + ")"
		}
		return "](" + siteBaseURL + strings.TrimSuffix(docsPathPrefix, "/") + p + ")"
	})
}
