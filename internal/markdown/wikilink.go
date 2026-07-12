package markdown

import (
	stdhtml "html"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/0spoon/seamless/internal/core"
)

// resolverKey carries the per-Render WikiResolver through the parser context so
// the inline parser can resolve a [[name]] at parse time and bake the result
// onto the AST node.
var resolverKey = parser.NewContextKey()

// wikiLinkKind is the AST node kind for a [[name]] reference.
var wikiLinkKind = ast.NewNodeKind("SeamlessWikiLink")

// wikiLink is an inline node for a [[name]] reference, resolved to a peek link
// or left as its literal token.
type wikiLink struct {
	ast.BaseInline
	href     string // resolved target href (valid when resolved)
	display  string // anchor text: the |alias if given, else the bare name
	raw      string // the literal "[[...]]" token, rendered when unresolved
	resolved bool
}

func (n *wikiLink) Kind() ast.NodeKind { return wikiLinkKind }

func (n *wikiLink) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"display": n.display, "href": n.href}, nil)
}

// wikiLinkParser parses [[name]], [[project/name]], [[name|alias]], and
// [[name#anchor]] into a wikiLink node, resolving the target via the context
// resolver at parse time. It is registered ahead of goldmark's link parser (both
// fire on '['); it returns nil for anything that is not a "[[" so ordinary
// [text](url) links still reach the link parser. Because it runs at parse time,
// brackets inside code spans and fenced blocks are never seen here and stay
// literal.
type wikiLinkParser struct{}

func (wikiLinkParser) Trigger() []byte { return []byte{'['} }

func (wikiLinkParser) Parse(_ ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, _ := block.PeekLine()
	if len(line) < 5 || line[0] != '[' || line[1] != '[' { // shortest is "[[x]]"
		return nil
	}
	// A wiki reference contains no ']' (matching core's wikiLinkRe), so the inner
	// text runs to the first ']', which must be immediately followed by a second.
	end := -1
	for i := 2; i < len(line)-1; i++ {
		if line[i] == ']' {
			end = i
			break
		}
	}
	if end < 0 || line[end+1] != ']' {
		return nil
	}
	inner := string(line[2:end])
	name := core.WikiLinkName(inner)
	if name == "" {
		return nil
	}
	block.Advance(end + 2)

	n := &wikiLink{raw: string(line[:end+2]), display: wikiDisplay(inner, name)}
	if resolve, ok := pc.Get(resolverKey).(WikiResolver); ok && resolve != nil {
		if href, found := resolve(name); found {
			n.href, n.resolved = href, true
		}
	}
	return n
}

// wikiDisplay is the anchor text for a reference: the trimmed |alias when one is
// given, otherwise the normalized bare name.
func wikiDisplay(inner, name string) string {
	if _, after, found := strings.Cut(inner, "|"); found {
		if alias := strings.TrimSpace(after); alias != "" {
			return alias
		}
	}
	return name
}

// wikiLinkRenderer writes a wikiLink node: a data-peek anchor when resolved, else
// the literal (escaped) [[...]] token so unresolved references read as plain
// text. The raw HTML it emits is re-sanitized by the bluemonday pass in Render.
type wikiLinkRenderer struct{}

func (wikiLinkRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(wikiLinkKind, renderWikiLink)
}

func renderWikiLink(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*wikiLink)
	if n.resolved {
		_, _ = w.WriteString(`<a href="`)
		_, _ = w.WriteString(stdhtml.EscapeString(n.href))
		_, _ = w.WriteString(`" data-peek>`)
		_, _ = w.WriteString(stdhtml.EscapeString(n.display))
		_, _ = w.WriteString(`</a>`)
	} else {
		_, _ = w.WriteString(stdhtml.EscapeString(n.raw))
	}
	return ast.WalkSkipChildren, nil
}

// wikiLinkExt wires the [[name]] parser and renderer into a goldmark instance.
type wikiLinkExt struct{}

func (wikiLinkExt) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(
		util.Prioritized(wikiLinkParser{}, 100), // ahead of the link parser (200)
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(wikiLinkRenderer{}, 100),
	))
}

var wikiLinkExtension = wikiLinkExt{}
