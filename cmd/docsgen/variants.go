package main

import (
	"fmt"
	"strings"
)

// This file implements the docs' authored variant containers -- the build-time
// half of the OS/client context picker. Authors gate a span of markdown on the
// reader's context with a fence-style container:
//
//	::: when os=windows client=codex
//	...markdown...
//	:::
//
// expandVariants rewrites the container to a div the picker's CSS can show or
// hide off <html data-os> / <html data-clients>, prefixed with always-visible
// label chips so a filtered-out-of-view reader (no JS, llms, "All") still
// knows which context a block belongs to. textifyVariants is the sibling
// transform for the markdown representations, where the label becomes a bold
// line. Both are fence-aware, so a literal `::: when` inside a code sample is
// never mangled.

// variantDimension is one picker axis: its data attribute, its canonical value
// order (display order, deliberately not alphabetical), and the chip labels.
type variantDimension struct {
	key    string // authoring key and data-ctx-* suffix
	order  []string
	labels map[string]string
}

var variantDimensions = []variantDimension{
	{
		key:   "os",
		order: []string{"macos", "linux", "windows"},
		labels: map[string]string{
			"macos": "macOS", "linux": "Linux", "windows": "Windows",
		},
	},
	{
		key:   "client",
		order: []string{"claude", "claude-desktop", "codex"},
		labels: map[string]string{
			"claude": "Claude Code", "claude-desktop": "Claude app chat", "codex": "Codex",
		},
	},
}

const variantOpenPrefix = "::: when "

// variantSpec is one parsed container opener: per dimension, the values it
// names, already deduplicated and in canonical order.
type variantSpec struct {
	values map[string][]string
}

// parseVariantSpec parses the attribute list of a `::: when` opener.
func parseVariantSpec(attrs string) (variantSpec, error) {
	spec := variantSpec{values: map[string][]string{}}
	fields := strings.Fields(attrs)
	if len(fields) == 0 {
		return spec, fmt.Errorf("variant container needs at least one os= or client= condition")
	}
	for _, field := range fields {
		key, list, ok := strings.Cut(field, "=")
		if !ok {
			return spec, fmt.Errorf("malformed condition %q (want key=value,value)", field)
		}
		dim := variantDimensionByKey(key)
		if dim == nil {
			return spec, fmt.Errorf("unknown dimension %q (want os or client)", key)
		}
		if _, dup := spec.values[key]; dup {
			return spec, fmt.Errorf("dimension %q given twice", key)
		}
		seen := map[string]bool{}
		for v := range strings.SplitSeq(list, ",") {
			v = strings.TrimSpace(v)
			if dim.labels[v] == "" {
				return spec, fmt.Errorf("unknown %s value %q (want one of %s)", key, v, strings.Join(dim.order, ", "))
			}
			seen[v] = true
		}
		// Canonical order keeps the emitted attributes and chips deterministic
		// no matter how the author ordered the list.
		var ordered []string
		for _, v := range dim.order {
			if seen[v] {
				ordered = append(ordered, v)
			}
		}
		spec.values[key] = ordered
	}
	return spec, nil
}

func variantDimensionByKey(key string) *variantDimension {
	for i := range variantDimensions {
		if variantDimensions[i].key == key {
			return &variantDimensions[i]
		}
	}
	return nil
}

// openHTML renders the container's opening div plus its chip label line. No
// blank line between them: both belong to one CommonMark HTML block, and the
// blank line the caller appends afterwards is what lets the contained
// markdown render as markdown.
func (s variantSpec) openHTML() string {
	var b strings.Builder
	b.WriteString(`<div class="ctx-variant"`)
	for _, dim := range variantDimensions {
		if vals := s.values[dim.key]; len(vals) > 0 {
			b.WriteString(` data-ctx-` + dim.key + `="` + strings.Join(vals, " ") + `"`)
		}
	}
	b.WriteString(">\n" + `<p class="ctx-label">`)
	for _, dim := range variantDimensions {
		for _, v := range s.values[dim.key] {
			b.WriteString(`<span class="ctx-chip">` + dim.labels[v] + `</span>`)
		}
	}
	b.WriteString("</p>")
	return b.String()
}

// textLabel renders the container's label for text consumers, e.g.
// "**Windows · Codex:**".
func (s variantSpec) textLabel() string {
	var parts []string
	for _, dim := range variantDimensions {
		for _, v := range s.values[dim.key] {
			parts = append(parts, dim.labels[v])
		}
	}
	return "**" + strings.Join(parts, " · ") + ":**"
}

// expandVariants rewrites every variant container in a page's markdown to its
// HTML form and reports whether the page has any -- that is what gates the
// picker bar into the layout. Errors carry the 1-based source line: an
// unclosed or misspelled container must fail the build, not ship silently.
func expandVariants(md string) (string, bool, error) {
	var out []string
	inFence := false
	openLine := 0 // 1-based line of the currently open container; 0 = none
	found := false
	lineNo := 0
	for line := range strings.SplitSeq(md, "\n") {
		lineNo++
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}
		if inFence || !strings.HasPrefix(trimmed, ":::") {
			if openLine > 0 && !inFence && (strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ")) {
				return "", false, fmt.Errorf("line %d: heading inside a variant container (headings feed the TOC, which must never anchor into a hidden block -- the chips are the label)", lineNo)
			}
			out = append(out, line)
			continue
		}
		switch {
		case trimmed == ":::":
			if openLine == 0 {
				return "", false, fmt.Errorf("line %d: ::: closer with no open variant container", lineNo)
			}
			openLine = 0
			out = append(out, "", "</div>")
		case strings.HasPrefix(trimmed, variantOpenPrefix):
			if openLine > 0 {
				return "", false, fmt.Errorf("line %d: variant containers cannot nest (container open since line %d)", lineNo, openLine)
			}
			spec, err := parseVariantSpec(strings.TrimPrefix(trimmed, variantOpenPrefix))
			if err != nil {
				return "", false, fmt.Errorf("line %d: %w", lineNo, err)
			}
			openLine = lineNo
			found = true
			out = append(out, spec.openHTML(), "")
		default:
			return "", false, fmt.Errorf("line %d: malformed variant marker %q (want %q or %q)", lineNo, trimmed, variantOpenPrefix+"os=... client=...", ":::")
		}
	}
	if openLine > 0 {
		return "", false, fmt.Errorf("line %d: variant container never closed", openLine)
	}
	return strings.Join(out, "\n"), found, nil
}

// textifyVariants rewrites variant containers for the markdown
// representations (twins, llms-full.txt, search text): the opener becomes its
// bold label line, the closer drops, and the contained markdown stays -- all
// variants present and labeled, never hidden.
func textifyVariants(md string) string {
	var out []string
	inFence := false
	for line := range strings.SplitSeq(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}
		if inFence || !strings.HasPrefix(trimmed, ":::") {
			out = append(out, line)
			continue
		}
		if trimmed == ":::" {
			continue
		}
		if spec, err := parseVariantSpec(strings.TrimPrefix(trimmed, variantOpenPrefix)); err == nil && strings.HasPrefix(trimmed, variantOpenPrefix) {
			out = append(out, spec.textLabel())
		}
		// A malformed marker is expandVariants' build error to report; the
		// text pass just drops it rather than double-reporting.
	}
	return strings.Join(out, "\n")
}
