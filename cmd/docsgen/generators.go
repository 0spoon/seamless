package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/config"
	seamlessmcp "github.com/0spoon/seamless/internal/mcp"
)

// A generator emits markdown that renderPages appends to a page's authored body,
// so generated content flows through goldmark like prose and inherits heading
// anchors, the TOC rail, and the search index for free.
//
// Generators read the code they document -- the MCP catalog, the config defaults
// -- rather than a transcription of it. That is the point: a tool or key that
// changes shows up in the docs on the next `make docs`, and `make docs-check`
// fails the build if nobody ran it.
type generator func(p *Page, srcDir string) (string, error)

var generators = map[string]generator{
	"mcp-tools": generateMCPTools,
	"config":    generateConfig,
}

func generate(name string, p *Page, srcDir string) (string, error) {
	g, ok := generators[name]
	if !ok {
		known := make([]string, 0, len(generators))
		for k := range generators {
			known = append(known, k)
		}
		sort.Strings(known)
		return "", fmt.Errorf("unknown generator (known: %s)", strings.Join(known, ", "))
	}
	return g(p, srcDir)
}

// ---------------------------------------------------------------------------
// mcp-tools

// toolPartialDir holds optional per-tool supplements: docs-src/.../_tools/<name>.md
// is appended inside that tool's generated section. The underscore marks it a
// partial, so nav validation does not demand it appear in the sidebar (see
// isPartial). Generated material states what a tool takes; the partial is where
// a real captured call, its response, and its failure modes go -- those must be
// written by hand from a live instance, never invented by a generator.
const toolPartialDir = "_tools"

func generateMCPTools(p *Page, srcDir string) (string, error) {
	if len(p.Tools) == 0 {
		return "", fmt.Errorf("generator mcp-tools needs a `tools:` list in the page frontmatter")
	}
	catalog := make(map[string]mcp.Tool, len(seamlessmcp.Catalog()))
	for _, t := range seamlessmcp.Catalog() {
		catalog[t.Name] = t
	}

	var b strings.Builder
	for _, name := range p.Tools {
		tool, ok := catalog[name]
		if !ok {
			return "", fmt.Errorf("no such MCP tool %q (it must appear in mcp.Catalog())", name)
		}
		// Pin the anchor to the tool's real name: /reference/mcp/tasks/#tasks_claim.
		fmt.Fprintf(&b, "## %s {#%s}\n\n%s\n\n", tool.Name, tool.Name, escapeText(tool.Description))

		params, err := toolParams(tool)
		if err != nil {
			return "", fmt.Errorf("%s: %w", tool.Name, err)
		}
		if len(params) == 0 {
			b.WriteString("Takes no parameters.\n\n")
		} else {
			b.WriteString("| Parameter | Type | Required | Description |\n|---|---|---|---|\n")
			for _, param := range params {
				required := "no"
				if param.required {
					required = "**yes**"
				}
				fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", param.name, param.typeName, required, param.description)
			}
			b.WriteString("\n")
		}

		partial, err := readPartial(filepath.Join(srcDir, filepath.Dir(p.Src), toolPartialDir, name+".md"))
		if err != nil {
			return "", err
		}
		if partial != "" {
			b.WriteString(partial)
			b.WriteString("\n\n")
		}
	}
	return b.String(), nil
}

// toolParam is one row of a tool's parameter table.
type toolParam struct {
	name        string
	typeName    string
	description string
	required    bool
}

// toolParams flattens a tool's JSON-Schema properties into table rows: required
// parameters first in their declared order, then the optional ones sorted by
// name (so the output is stable regardless of Go's map iteration).
//
// Every unrecognized schema shape is an error, not a skipped row. An mcp-go
// upgrade that reshapes Properties must fail the build loudly rather than
// quietly emit empty parameter tables that read as "this tool takes nothing".
func toolParams(tool mcp.Tool) ([]toolParam, error) {
	required := make(map[string]bool, len(tool.InputSchema.Required))
	for _, name := range tool.InputSchema.Required {
		if _, ok := tool.InputSchema.Properties[name]; !ok {
			return nil, fmt.Errorf("schema requires %q but declares no such property", name)
		}
		required[name] = true
	}

	optional := make([]string, 0, len(tool.InputSchema.Properties))
	for name := range tool.InputSchema.Properties {
		if !required[name] {
			optional = append(optional, name)
		}
	}
	sort.Strings(optional)

	names := append(append([]string{}, tool.InputSchema.Required...), optional...)
	out := make([]toolParam, 0, len(names))
	for _, name := range names {
		spec, ok := tool.InputSchema.Properties[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("property %q: expected a JSON-Schema object, got %T", name, tool.InputSchema.Properties[name])
		}
		typeName, ok := spec["type"].(string)
		if !ok {
			return nil, fmt.Errorf("property %q: schema has no string \"type\"", name)
		}
		desc, _ := spec["description"].(string)
		if enum, ok := spec["enum"]; ok {
			values, err := enumValues(enum)
			if err != nil {
				return nil, fmt.Errorf("property %q: %w", name, err)
			}
			desc = strings.TrimRight(strings.TrimSpace(desc), ".") + ". One of: " + values + "."
			desc = strings.TrimPrefix(desc, ". ")
		}
		out = append(out, toolParam{
			name:        name,
			typeName:    typeName,
			description: escapeCell(desc),
			required:    required[name],
		})
	}
	return out, nil
}

// enumValues renders a property's allowed values. mcp.Enum() builds []string,
// but a schema round-tripped through JSON arrives as []any, and both are valid
// inputs -- so accept either and reject anything else.
func enumValues(enum any) (string, error) {
	var values []string
	switch list := enum.(type) {
	case []string:
		values = list
	case []any:
		for _, v := range list {
			s, ok := v.(string)
			if !ok {
				return "", fmt.Errorf("enum: expected string values, got %T", v)
			}
			values = append(values, s)
		}
	default:
		return "", fmt.Errorf("enum: expected a list of strings, got %T", enum)
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, "`"+v+"`")
	}
	return strings.Join(out, ", "), nil
}

// escapeText makes a plain-text string from the code (a tool description, a
// config default) safe to embed in generated markdown.
//
// The docs renderer runs with html.WithUnsafe, because authored pages embed raw
// HTML. Generated text is not authored: descriptions like "pass plan=<slug>"
// would otherwise reach the browser as an unknown <slug> element and render as
// nothing at all -- the text silently disappears rather than failing loudly. So
// every generated string is HTML-escaped before it becomes markdown.
func escapeText(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// escapeCell makes text safe inside a GFM table cell: escapeText, plus pipes and
// newlines, either of which would shred the row into extra columns or end the
// table early. Pipes are not hypothetical -- several tool descriptions spell out
// their accepted values as "startup|resume|clear|compact|explicit".
func escapeCell(s string) string {
	s = escapeText(s)
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.Join(strings.Fields(s), " ")
}

// escapeCodeCell escapes a value that will sit inside a code span in a table
// cell. Only the pipe needs escaping: goldmark HTML-escapes code-span content
// itself, so running escapeText here too would double-encode and publish a
// default of "&amp;lt;" where the code says "<".
func escapeCodeCell(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "|", "\\|")), " ")
}

// readPartial returns a partial's contents, or "" when it does not exist.
func readPartial(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read partial %s: %w", path, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// ---------------------------------------------------------------------------
// config

func generateConfig(_ *Page, _ string) (string, error) {
	var b strings.Builder
	b.WriteString("## Every key\n\n")
	b.WriteString("| Key | Type | Default |\n|---|---|---|\n")

	rows, err := configRows(reflect.ValueOf(config.Defaults()), "")
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		b.WriteString(r)
	}

	example, err := os.ReadFile("seamless.yaml.example")
	if err != nil {
		return "", fmt.Errorf("read seamless.yaml.example (docsgen must run from the repo root): %w", err)
	}
	b.WriteString("\n## Annotated example\n\n")
	b.WriteString("This is `seamless.yaml.example` from the repository, verbatim.\n\n")
	b.WriteString("```yaml\n")
	b.WriteString(strings.TrimRight(string(example), "\n"))
	b.WriteString("\n```\n")
	return b.String(), nil
}

// configRows walks a config struct in field-declaration order (never map order)
// emitting one table row per leaf key, recursing into nested config structs to
// build dotted paths like `llm.openai.chat_model`.
func configRows(v reflect.Value, prefix string) ([]string, error) {
	var rows []string
	t := v.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue // e.g. Config.sourcePath: internal bookkeeping, not a key
		}
		tag, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		key := tag
		if prefix != "" {
			key = prefix + "." + tag
		}
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct {
			nested, err := configRows(fv, key)
			if err != nil {
				return nil, err
			}
			rows = append(rows, nested...)
			continue
		}
		rows = append(rows, fmt.Sprintf("| `%s` | %s | %s |\n", key, fv.Type(), formatDefault(fv)))
	}
	return rows, nil
}

// formatDefault renders a default value for the table. A zero value prints as a
// dash rather than `""` or `0`: the useful statement is "no default", and an
// empty-looking code span reads like a typo.
func formatDefault(v reflect.Value) string {
	if v.IsZero() {
		return "—"
	}
	switch v.Kind() {
	case reflect.String:
		return "`" + escapeCodeCell(v.String()) + "`"
	case reflect.Slice:
		parts := make([]string, 0, v.Len())
		for i := range v.Len() {
			parts = append(parts, fmt.Sprintf("%v", v.Index(i).Interface()))
		}
		return "`[" + strings.Join(parts, ", ") + "]`"
	default:
		return fmt.Sprintf("`%v`", v.Interface())
	}
}
