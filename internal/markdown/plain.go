package markdown

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// PlainText renders a markdown body to a single collapsed line of plain text:
// markdown syntax and [[wiki]] brackets are stripped and all whitespace is
// collapsed to single spaces. It is for clamped list previews, where formatting
// would only add noise. Wiki references render as their display text (alias or
// bare name), never as bare brackets.
func PlainText(body string) string {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	src := []byte(body)
	doc := md.Parser().Parse(text.NewReader(src))
	var b strings.Builder
	appendText(&b, doc, src)
	return strings.Join(strings.Fields(b.String()), " ")
}

// appendText walks the AST depth-first, emitting the textual content of each
// node. Block nodes contribute a trailing space so words never join across a
// paragraph or list-item boundary; the caller collapses the result with
// strings.Fields.
func appendText(b *strings.Builder, n ast.Node, source []byte) {
	switch node := n.(type) {
	case *wikiLink:
		b.WriteString(node.display)
		return
	case *ast.Text:
		b.Write(node.Segment.Value(source))
		if node.SoftLineBreak() || node.HardLineBreak() {
			b.WriteByte(' ')
		}
		return
	case *ast.String:
		b.Write(node.Value)
		return
	case *ast.AutoLink:
		b.Write(node.URL(source))
		return
	case *ast.FencedCodeBlock:
		appendLines(b, node.Lines(), source)
		b.WriteByte(' ')
		return
	case *ast.CodeBlock:
		appendLines(b, node.Lines(), source)
		b.WriteByte(' ')
		return
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		appendText(b, c, source)
	}
	if n.Type() == ast.TypeBlock {
		b.WriteByte(' ')
	}
}

func appendLines(b *strings.Builder, segs *text.Segments, source []byte) {
	for i := 0; i < segs.Len(); i++ {
		seg := segs.At(i)
		b.Write(seg.Value(source))
	}
}
