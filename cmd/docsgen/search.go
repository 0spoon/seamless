package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/0spoon/seamless/internal/markdown"
)

// searchTextRunes caps each page's indexed body text. The index is fetched whole
// by the browser on first search, so it trades tail-of-page recall for a small
// download; titles and headings (never truncated) carry most of the signal.
const searchTextRunes = 2000

// searchDoc is one page in static/search-index.json. Field order is the JSON key
// order; the client scores title > headings > text.
type searchDoc struct {
	Title    string   `json:"title"`
	Section  string   `json:"section"`
	URL      string   `json:"url"` // relative to the docs root; the client prefixes it
	Headings []string `json:"headings"`
	Text     string   `json:"text"`
}

// writeSearchIndex emits the client search index in nav order.
func writeSearchIndex(outDir string, site *Site) error {
	docs := make([]searchDoc, 0, len(site.Pages))
	for _, p := range site.Pages {
		headings := make([]string, 0, len(p.Headings))
		for _, h := range p.Headings {
			headings = append(headings, h.Text)
		}
		docs = append(docs, searchDoc{
			Title:    p.Title,
			Section:  p.Section.Title,
			URL:      p.URL,
			Headings: headings,
			Text:     p.Text,
		})
	}
	data, err := json.Marshal(docs)
	if err != nil {
		return fmt.Errorf("encode search index: %w", err)
	}
	return writeFile(filepath.Join(outDir, "static", "search-index.json"), data)
}

// plainText renders a markdown body to capped, collapsed plain text for the
// search index, reusing the console's renderer -- it only reads, so unlike
// markdown.Render it carries no sanitizer that would fight docsgen's output.
func plainText(md string) string {
	text := markdown.PlainText(md)
	runes := []rune(text)
	if len(runes) <= searchTextRunes {
		return text
	}
	return string(runes[:searchTextRunes])
}
