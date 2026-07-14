// Package importer migrates Seam v1 data (~/.seam) into Seamless. v1 encoded
// memories as notes with "Knowledge: {kind} - {name}" titles and domain:/project:/
// session: tags; this package decodes that convention back into first-class
// memory frontmatter, normalizes plain notes, lifts trial notes into trials
// rows, and replays agent_sessions + agent_tool_calls as sessions + events. It
// reads v1 (files + a seam.db snapshot) and writes v2; it never modifies v1.
package importer

import (
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/0spoon/seamless/internal/core"
)

// v1Frontmatter mirrors the fields Seam v1 wrote into note frontmatter.
type v1Frontmatter struct {
	ID               string   `yaml:"id"`
	Title            string   `yaml:"title"`
	Description      string   `yaml:"description"`
	Project          string   `yaml:"project"`
	Tags             []string `yaml:"tags"`
	Created          string   `yaml:"created"`
	Modified         string   `yaml:"modified"`
	SourceURL        string   `yaml:"source_url"`
	TranscriptSource bool     `yaml:"transcript_source"`
}

// splitFrontmatter splits a v1 markdown file into its YAML frontmatter text and
// body. Files without a leading "---" delimiter have empty frontmatter.
func splitFrontmatter(content string) (yamlText, body string) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return "", content
	}
	rest := content[4:]
	y, b, found := strings.Cut(rest, "\n---")
	if !found {
		return "", content
	}
	return y, strings.TrimPrefix(b, "\n")
}

// parseV1 parses a v1 note file into its frontmatter and body.
func parseV1(content string) (v1Frontmatter, string, error) {
	yamlText, body := splitFrontmatter(content)
	var fm v1Frontmatter
	if yamlText != "" {
		if err := yaml.Unmarshal([]byte(yamlText), &fm); err != nil {
			return v1Frontmatter{}, "", err
		}
	}
	return fm, body, nil
}

// itemClass is how a v1 note maps into v2.
type itemClass int

const (
	classNote   itemClass = iota // plain work artifact
	classMemory                  // "Knowledge: ..." title
	classTrial                   // type:trial tag / "Trial: ..." title
)

// classify decides what a v1 note becomes in v2 from its title and tags.
func classify(fm v1Frontmatter) itemClass {
	if strings.HasPrefix(fm.Title, "Knowledge:") {
		return classMemory
	}
	if hasTag(fm.Tags, "type:trial") || strings.HasPrefix(fm.Title, "Trial:") {
		return classTrial
	}
	return classNote
}

// hasTag reports whether tags contains an exact tag.
func hasTag(tags []string, want string) bool {
	return slices.Contains(tags, want)
}

// tagValue returns the value of the first tag with the given "prefix:" form.
func tagValue(tags []string, prefix string) string {
	for _, t := range tags {
		if v, ok := strings.CutPrefix(t, prefix+":"); ok {
			return v
		}
	}
	return ""
}

// parseTime parses a v1 timestamp (RFC3339), falling back to the zero time.
func parseTime(s string) time.Time {
	t, err := core.ParseTime(s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// memoryFromV1 maps a v1 "Knowledge:" note into a v2 memory. The title encodes
// kind + name ("Knowledge: {kind} - {name}"); the semantic project and provenance
// live in the tags (project:, session:). Returns the memory and any warning.
func memoryFromV1(fm v1Frontmatter, body string) (core.Memory, string) {
	var warning string
	rest := strings.TrimPrefix(fm.Title, "Knowledge:")
	rest = strings.TrimSpace(rest)
	catStr, name, ok := strings.Cut(rest, " - ")
	if !ok {
		// No " - " separator: treat the whole remainder as the name, kind unknown.
		name = rest
		catStr = tagValue(fm.Tags, "domain")
	}
	catStr = strings.TrimSpace(strings.ToLower(catStr))
	name = strings.TrimSpace(name)

	kind := core.MemoryKind(catStr)
	if !kind.Valid() {
		if d := core.MemoryKind(tagValue(fm.Tags, "domain")); d.Valid() {
			kind = d
		} else {
			warning = "unknown kind " + catStr + " -> reference"
			kind = core.KindReference
		}
	}

	created := parseTime(fm.Created)
	updated := parseTime(fm.Modified)
	if updated.IsZero() {
		updated = created
	}

	return core.Memory{
		ID:            fm.ID,
		Kind:          kind,
		Name:          core.Slugify(name),
		Description:   fm.Description,
		Project:       tagValue(fm.Tags, "project"),
		Body:          body,
		Tags:          filterMemoryTags(fm.Tags),
		Created:       created,
		Updated:       updated,
		ValidFrom:     created,
		SourceSession: tagValue(fm.Tags, "session"),
	}, warning
}

// filterMemoryTags drops the tags that become first-class fields (kind, project,
// provenance) or are redundant (type:), keeping the rest.
func filterMemoryTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		switch {
		case strings.HasPrefix(t, "domain:"),
			strings.HasPrefix(t, "project:"),
			strings.HasPrefix(t, "session:"),
			strings.HasPrefix(t, "type:"):
			continue
		default:
			out = append(out, t)
		}
	}
	return out
}

// noteFromV1 maps a v1 note into a v2 note. project comes from the storage tree
// segment (passed by the caller); slug comes from the filename.
func noteFromV1(fm v1Frontmatter, body, project, slug string) core.Note {
	created := parseTime(fm.Created)
	updated := parseTime(fm.Modified)
	if updated.IsZero() {
		updated = created
	}
	var extra map[string]any
	if fm.TranscriptSource {
		extra = map[string]any{"transcript_source": true}
	}
	return core.Note{
		ID:          fm.ID,
		Title:       fm.Title,
		Slug:        slug,
		Description: fm.Description,
		Project:     project,
		Body:        body,
		Tags:        fm.Tags,
		SourceURL:   fm.SourceURL,
		Created:     created,
		Updated:     updated,
		Extra:       extra,
	}
}

// trialFromV1 maps a v1 "Trial:" note into a v2 trial row. Lab, outcome, and the
// change/expected/actual sections are parsed out of the markdown body.
func trialFromV1(fm v1Frontmatter, body, project string) core.Trial {
	created := parseTime(fm.Created)
	lab := tagValue(fm.Tags, "lab")
	if lab == "" {
		lab = inlineField(body, "Lab")
	}
	return core.Trial{
		ID:          fm.ID,
		Lab:         lab,
		Title:       strings.TrimSpace(strings.TrimPrefix(fm.Title, "Trial:")),
		Changes:     section(body, "Changes"),
		Expected:    section(body, "Expected"),
		Actual:      section(body, "Actual"),
		Outcome:     core.TrialOutcome(strings.ToLower(inlineField(body, "Outcome"))),
		Metrics:     map[string]any{},
		ProjectSlug: project,
		CreatedAt:   created,
	}
}

// inlineField extracts the value of a "**Field:** value" line from a body.
func inlineField(body, field string) string {
	_, after, found := strings.Cut(body, "**"+field+":**")
	if !found {
		return ""
	}
	line, _, _ := strings.Cut(after, "\n")
	return strings.TrimSpace(line)
}

// section returns the text of a "## {header}" markdown section, up to the next
// "## " header or end of body. v1 sometimes runs the header into its content
// ("## Changesfoo"), so the header prefix is matched without requiring a break.
func section(body, header string) string {
	_, after, found := strings.Cut(body, "## "+header)
	if !found {
		return ""
	}
	if before, _, ok := strings.Cut(after, "\n## "); ok {
		after = before
	}
	return strings.TrimSpace(after)
}
