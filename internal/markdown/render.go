// Package markdown renders agent-authored markdown as sanitized HTML for the
// Seamless console. Agents write their durable knowledge -- memory, note, and
// task bodies, session findings, gardener digests -- in markdown; the console
// shows it. Render turns that markdown into safe template.HTML server-side (so
// the no-JS detail pages keep working), preserving the [[wiki-link]] cross-links
// that open the peek drawer. PlainText strips markdown for one-line previews.
//
// Safety is layered: goldmark runs with raw inline/block HTML disabled
// (html.WithUnsafe is left OFF), and every rendered fragment then passes through
// a bluemonday UGC policy -- the real trust boundary -- so any HTML an agent
// embeds in a body is neutralized regardless of the parser's settings.
package markdown

import (
	"bytes"
	"html/template"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

// WikiResolver maps a normalized [[name]] reference to an href. ok=false leaves
// the reference as its literal "[[name]]" text. A nil resolver (passed to
// Render) disables wiki-linking entirely.
type WikiResolver func(name string) (href string, ok bool)

// md is the shared markdown engine, built once. GFM adds tables, strikethrough,
// task lists, and autolinks; wikiLinkExtension adds [[name]] parsing. Raw HTML
// stays disabled (WithUnsafe OFF) -- the bluemonday pass in Render is the safety
// boundary.
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM, wikiLinkExtension),
)

// policy sanitizes rendered HTML. It starts from bluemonday's UGC policy -- which
// already restricts URLs to http/https/mailto plus relative links -- and adds the
// data-peek attribute on <a> (so wiki-link anchors survive) and the align
// attribute on table cells (GFM column alignment).
var policy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("data-peek").OnElements("a")
	p.AllowAttrs("align").OnElements("td", "th")
	// The console renders its own trusted store; rel=nofollow adds nothing on a
	// local single-user tool and only makes body anchors diverge from the ones
	// the templates emit directly.
	p.RequireNoFollowOnLinks(false)
	return p
}()

// Render turns an agent-authored markdown body into sanitized HTML. [[name]]
// references are resolved to peek links via resolve (nil disables wiki-linking);
// unresolved references stay as literal text. The result is safe to embed
// directly in a template.
func Render(body string, resolve WikiResolver) template.HTML {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	pc := parser.NewContext()
	if resolve != nil {
		pc.Set(resolverKey, resolve)
	}
	var buf bytes.Buffer
	if err := md.Convert([]byte(body), &buf, parser.WithContext(pc)); err != nil {
		// Convert only fails on a writer error, and bytes.Buffer never errors, so
		// this is unreachable in practice; degrade to escaped plain text.
		return template.HTML(template.HTMLEscapeString(body))
	}
	return template.HTML(policy.SanitizeBytes(buf.Bytes()))
}
