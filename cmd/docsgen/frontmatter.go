package main

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// pageMeta is a page's YAML frontmatter. Title is required -- it names the page
// in the sidebar, the <title>, the search index, and the prev/next rail, and a
// page without one is an authoring mistake, not a page to title "Untitled".
type pageMeta struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	// Generate names a generator (see generators.go) whose markdown is appended
	// to the authored body. Empty means a purely authored page.
	Generate string `yaml:"generate"`
	// Tools is the generate: mcp-tools argument: which tools this page documents,
	// in the order they should appear. The reference splits the catalog across
	// several group pages, so each names its own slice.
	Tools []string `yaml:"tools"`
}

var frontmatterFence = []byte("---")

// splitFrontmatter separates a leading `---`-fenced YAML block from the markdown
// body. Frontmatter is mandatory: every docs-src page needs at least a title, so
// a missing fence is a build error rather than a silently untitled page.
func splitFrontmatter(src []byte) (pageMeta, string, error) {
	var meta pageMeta

	rest, ok := bytes.CutPrefix(src, frontmatterFence)
	if !ok || (len(rest) > 0 && rest[0] != '\n' && rest[0] != '\r') {
		return meta, "", fmt.Errorf("missing frontmatter: file must start with a --- fenced YAML block")
	}
	// Find the closing fence at the start of a line.
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return meta, "", fmt.Errorf("unterminated frontmatter: no closing --- fence")
	}
	yamlSrc := rest[:end]
	body := rest[end+len("\n---"):]
	// Drop the remainder of the closing fence line.
	if nl := bytes.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	} else {
		body = nil
	}

	if err := yaml.Unmarshal(yamlSrc, &meta); err != nil {
		return meta, "", fmt.Errorf("frontmatter: %w", err)
	}
	if strings.TrimSpace(meta.Title) == "" {
		return meta, "", fmt.Errorf("frontmatter: title is required")
	}
	return meta, string(body), nil
}
