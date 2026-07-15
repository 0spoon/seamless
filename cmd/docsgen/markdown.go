package main

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// Heading is one entry in a page's "On this page" rail.
type Heading struct {
	Level int // 2 or 3
	ID    string
	Text  string
}

// docsMD is this command's own goldmark engine, deliberately NOT
// internal/markdown.Render.
//
// internal/markdown renders agent-authored content -- untrusted input from the
// store -- and its bluemonday pass would strip the class attributes chroma emits
// (<span class="k">), leaving syntax highlighting silently unstyled. docs-src is
// repo-authored and reviewed, so raw HTML is allowed (WithUnsafe) and no
// sanitizer runs. That trust boundary is the whole reason for a second engine;
// it holds only while docs-src stays a repo-authored tree.
//
// PlainText from internal/markdown IS reused (see search.go): it only reads.
var docsMD = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
		// Highlight at build time into CSS classes -- no client JS, and the theme
		// follows the site's own tokens (see the chroma block in docs.css). Chroma's
		// own stylesheets are not generated; assets/docs.css maps the class names
		// to --syn-* variables for both light and dark.
		highlighting.NewHighlighting(
			highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
		),
	),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
		// Enables `## tasks_claim {#tasks_claim}`. The auto-id algorithm rewrites
		// underscores to dashes, which would publish /reference/mcp/tasks/#tasks-claim
		// for a tool literally named tasks_claim. Anchors are a promised, linkable
		// surface, so generated pages pin their own ids and take the auto-id only
		// for prose headings.
		parser.WithHeadingAttribute(),
	),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

// rendered is one page's markdown output.
type rendered struct {
	HTML     template.HTML
	Headings []Heading
	// Links are the same-site absolute paths the body referenced, fragments
	// stripped, in document order. checkLinks resolves them against the site.
	Links []string
}

// renderMarkdown converts an authored body to HTML, extracts its h2/h3 outline
// for the table of contents, and reports the internal links it made.
//
// docsRoot is the page's relative prefix to the docs root; see rewriteDocLinks.
func renderMarkdown(src, docsRoot string) (rendered, error) {
	source := []byte(src)
	doc := docsMD.Parser().Parse(text.NewReader(source))
	links, err := rewriteDocLinks(doc, docsRoot)
	if err != nil {
		return rendered{}, err
	}

	var buf bytes.Buffer
	if err := docsMD.Renderer().Render(&buf, source, doc); err != nil {
		return rendered{}, fmt.Errorf("render markdown: %w", err)
	}
	return rendered{
		HTML:     template.HTML(buf.Bytes()), //nolint:gosec // docs-src is repo-authored; see docsMD
		Headings: collectHeadings(doc, source),
		Links:    links,
	}, nil
}

// rewriteDocLinks turns root-absolute doc links into page-relative ones:
// `[memory](/concepts/memory/)` becomes ../concepts/memory/, ../../concepts/...,
// or concepts/... depending on how deep the linking page sits.
//
// The site publishes no base URL -- every href is relative, so it works at
// thereisnospoon.org/docs/, at the project-pages fallback, and under
// `make docs-serve`. Making authors hand-compute ../../ per link would be a
// silent rot generator: the link still renders, it just goes nowhere, and only a
// human clicking it would ever notice. So authors write one absolute path and
// the depth arithmetic happens here, once.
//
// Only same-site paths are touched. Scheme-relative (//host), absolute URLs,
// fragments, and already-relative links pass through untouched.
// It also reports every same-site path it rewrote, so checkLinks can prove each
// one resolves to a page that exists.
func rewriteDocLinks(doc ast.Node, docsRoot string) ([]string, error) {
	var links []string
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var dest *[]byte
		switch link := n.(type) {
		case *ast.Link:
			dest = &link.Destination
		case *ast.Image:
			dest = &link.Destination
		default:
			return ast.WalkContinue, nil
		}
		s := string(*dest)
		if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") {
			return ast.WalkContinue, nil
		}
		links = append(links, s)
		*dest = []byte(docsRoot + strings.TrimPrefix(s, "/"))
		return ast.WalkContinue, nil
	})
	return links, err
}

// collectHeadings walks the parsed document for h2/h3 headings, reading the ids
// goldmark's auto-heading-id already assigned so the TOC anchors and the
// rendered heading anchors cannot disagree.
func collectHeadings(doc ast.Node, source []byte) []Heading {
	var out []Heading
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		h, ok := n.(*ast.Heading)
		if !ok || h.Level < 2 || h.Level > 3 {
			continue
		}
		id, ok := h.AttributeString("id")
		if !ok {
			continue
		}
		idBytes, ok := id.([]byte)
		if !ok {
			continue
		}
		out = append(out, Heading{
			Level: h.Level,
			ID:    string(idBytes),
			Text:  headingText(h, source),
		})
	}
	return out
}

// headingText flattens a heading's inline children to plain text. ast.Node.Text
// is deprecated in goldmark 1.8, so walk the inlines: a heading's text is Text
// segments plus the literal String nodes that code spans and typographic
// substitutions contribute.
func headingText(h *ast.Heading, source []byte) string {
	var b strings.Builder
	err := ast.Walk(h, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := n.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(source))
		case *ast.String:
			b.Write(t.Value)
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		// The visitor above never errors, so this is unreachable; degrade to the
		// text collected so far rather than dropping the heading from the TOC.
		return b.String()
	}
	return b.String()
}
