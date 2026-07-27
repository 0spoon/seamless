package main

import (
	"html"
	"regexp"
	"strings"
)

// This file emits each docs page's markdown twin: an index.md written next to
// the page's index.html, holding the same untruncated source markdown that
// llms-full.txt aggregates -- split back out per page so the CDN can serve it
// for `Accept: text/markdown` content negotiation (see llmstxt.org and
// Cloudflare's "Markdown for Agents"). The negotiation itself is one Transform
// Rule at the edge (documented in SITE.md): markdown-accepting requests for
// negotiable directory URLs rewrite to their sibling index.md, which GitHub
// Pages already serves as text/markdown. The site root gets the same twin
// treatment via siteRootFiles. HTML stays the default for every request that
// does not ask.

// rootLinkRE matches the destination of an inline markdown link or image whose
// path is root-absolute. Authored destinations never contain spaces or nested
// parentheses (checkLinks resolves every one of them), so the simple form is
// exact for this tree.
var rootLinkRE = regexp.MustCompile(`\]\((/[^)\s]+)\)`)

// figureRE matches one authored explanatory figure block. Figures never nest
// and always close (site_test.go pins the markup shape), so the non-greedy
// span is exact.
var figureRE = regexp.MustCompile(`(?s)<figure class="doc-figure".*?</figure>`)

var brTagRE = regexp.MustCompile(`<br\s*/?>`)

var anyTagRE = regexp.MustCompile(`<[^>]+>`)

// cardGridRE matches one authored router card grid (the docs home's "which
// agent do you run?" cards). Authored grids contain only anchor cards -- no
// nested divs -- so the non-greedy span ends at the grid's own closer.
var cardGridRE = regexp.MustCompile(`(?s)<div class="card-grid">.*?</div>`)

// docCardRE matches one card inside a grid: href, title, optional description.
var docCardRE = regexp.MustCompile(`(?s)<a class="doc-card" href="([^"]+)">\s*<h2>(.*?)</h2>\s*(?:<p>(.*?)</p>\s*)?</a>`)

// htmlCommentRE matches an authoring note; text consumers never need those.
var htmlCommentRE = regexp.MustCompile(`(?s)<!--.*?-->\n?`)

// cfgRE matches the setup configurator widget. Its content is composed by JS
// from the picker state, so there is no reader text worth mirroring; the
// static labeled install blocks around it carry the commands.
var cfgRE = regexp.MustCompile(`(?s)<div class="cfg".*?</div>\n?`)

// detailsTagRE / summaryRE flatten native disclosures: the summary becomes a
// bold lead line and the content stays inline -- a text consumer must never
// receive less than the page shows expanded.
var (
	detailsTagRE = regexp.MustCompile(`</?details[^>]*>\n?`)
	summaryRE    = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
)

// textifyEmbeddedHTML flattens every authored affordance for the markdown
// representations (the per-page twins and llms-full.txt): figures become
// fenced text, card grids become link lists, variant containers become bold
// label lines over their still-present content, disclosures open into bold
// lead lines, the configurator widget and HTML comments drop.
func textifyEmbeddedHTML(md string) string {
	out := textifyCards(textifyFigures(textifyVariants(md)))
	out = cfgRE.ReplaceAllString(out, "")
	out = detailsTagRE.ReplaceAllString(out, "")
	out = summaryRE.ReplaceAllString(out, "**$1**")
	return htmlCommentRE.ReplaceAllString(out, "")
}

// textifyCards rewrites an authored card grid as the markdown link list its
// HTML shows. Card hrefs are authored docs-root-relative because raw HTML is
// invisible to rewriteDocLinks; they re-root here as root-absolute markdown
// links so absolutizeRootLinks treats them like every authored link.
func textifyCards(md string) string {
	return cardGridRE.ReplaceAllStringFunc(md, func(block string) string {
		var lines []string
		for _, m := range docCardRE.FindAllStringSubmatch(block, -1) {
			href, title, desc := m[1], strings.TrimSpace(m[2]), strings.TrimSpace(m[3])
			if !strings.HasPrefix(href, "/") && !strings.Contains(href, "://") {
				href = "/" + href
			}
			line := "- [" + title + "](" + href + ")"
			if desc != "" {
				line += ": " + desc
			}
			lines = append(lines, line)
		}
		return strings.Join(lines, "\n")
	})
}

// textifyFigures rewrites each explanatory figure as a fenced text block for
// the markdown representations (the per-page twins and llms-full.txt): <br>
// becomes a line break, every other tag a space, entities unescape, and blank
// lines drop. The figures' HTML is a browser rendering; a markdown consumer
// should get the same content as plain lines, not markup.
func textifyFigures(md string) string {
	return figureRE.ReplaceAllStringFunc(md, func(block string) string {
		text := brTagRE.ReplaceAllString(block, "\n")
		text = anyTagRE.ReplaceAllString(text, " ")
		text = html.UnescapeString(text)
		var lines []string
		for line := range strings.SplitSeq(text, "\n") {
			if fields := strings.Fields(line); len(fields) > 0 {
				lines = append(lines, strings.Join(fields, " "))
			}
		}
		return "```text\n" + strings.Join(lines, "\n") + "\n```"
	})
}

// markdownTwin renders a page's text/markdown representation: title,
// description blockquote, then the full source markdown with figures
// flattened to text -- the same per-page shape llms-full.txt uses. Section
// indexes append their card grid as a link list, mirroring what their HTML
// shows (generated ones have no body at all).
func markdownTwin(p *Page) []byte {
	var b strings.Builder
	b.WriteString("# " + p.Title + "\n")
	if p.Description != "" {
		b.WriteString("\n> " + p.Description + "\n")
	}
	if body := strings.TrimSpace(textifyEmbeddedHTML(p.FullMarkdown)); body != "" {
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
