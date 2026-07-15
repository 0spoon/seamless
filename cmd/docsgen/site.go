package main

import (
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// navFile is the information architecture manifest: docs-src/nav.yaml holds the
// whole site's section order and page order on one screen. It is the single
// source of nav truth -- loadSite cross-validates it against the files on disk
// in both directions, so the manifest and the tree cannot drift apart.
const navFile = "nav.yaml"

type navManifest struct {
	Sections []navSection `yaml:"sections"`
}

type navSection struct {
	Title string `yaml:"title"`
	// Slug is the section's directory under docs-src (and its URL segment).
	// Empty means the root section: its pages sit at the docs root.
	Slug        string `yaml:"slug"`
	Description string `yaml:"description"`
	// Pages are markdown paths relative to Slug, in nav order.
	Pages []string `yaml:"pages"`
}

// Site is the whole rendered documentation site.
type Site struct {
	// SrcDir is the authored source root; generators resolve their partials
	// relative to it.
	SrcDir   string
	Sections []*Section
	// Pages is every page in nav order -- the order the sidebar shows, prev/next
	// walks, and the search index uses. Deterministic by construction.
	Pages []*Page
	Home  *Page
}

// Section is one sidebar group.
type Section struct {
	Title       string
	Slug        string
	Description string
	// Index is the page at the section's own URL: authored if the manifest lists
	// <slug>/index.md, otherwise a generated card grid of Pages. The root section
	// has no separate index -- its index page is the site home.
	Index *Page
	Pages []*Page
}

// Page is one output HTML file.
type Page struct {
	Section *Section
	// Src is the docs-src-relative markdown path; empty for a generated section
	// index, which has no authored source.
	Src string
	// URL is the path relative to the docs root, always "" or "<segments>/":
	// "" (home), "quickstart/", "reference/mcp/tasks/".
	URL string
	// Out is the URL's file, relative to the output directory.
	Out string

	Title       string
	Description string
	Generate    string
	Tools       []string
	// Template names the layout body: "home", "section", or "page".
	Template string

	Markdown string
	Body     template.HTML
	Headings []Heading
	// Links are the same-site paths this page's body references; checkLinks
	// resolves them.
	Links []string
	// Text is the body as plain text, for the search index.
	Text string

	// Root and DocsRoot are relative href prefixes from this page's output file
	// to the site root (docs/, holding the shared landing-page assets) and to the
	// docs root (docs/docs/). Every href in every template is built from one of
	// them, so the site works at thereisnospoon.org/docs/, at the
	// 0spoon.github.io/seamless/docs/ fallback, and under `make docs-serve`
	// without a base URL setting anywhere.
	Root     string
	DocsRoot string

	Prev *Page
	Next *Page
}

// IsHome reports whether the page is the docs root.
func (p *Page) IsHome() bool { return p.URL == "" }

// loadSite parses nav.yaml, loads every listed page, cross-validates the
// manifest against the tree, and computes the derived nav state (URLs, relative
// roots, section indexes, prev/next). It does not render markdown; renderPages
// does, so validation errors surface before any HTML work.
func loadSite(srcDir string) (*Site, error) {
	manifestPath := filepath.Join(srcDir, navFile)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", manifestPath, err)
	}
	var manifest navManifest
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("%s: %w", manifestPath, err)
	}
	if len(manifest.Sections) == 0 {
		return nil, fmt.Errorf("%s: no sections", manifestPath)
	}

	site := &Site{SrcDir: srcDir}
	byURL := make(map[string]string) // URL -> the src that claimed it
	listed := make(map[string]bool)  // src paths named by the manifest

	for _, ns := range manifest.Sections {
		if strings.TrimSpace(ns.Title) == "" {
			return nil, fmt.Errorf("%s: a section is missing a title", navFile)
		}
		sec := &Section{Title: ns.Title, Slug: strings.Trim(ns.Slug, "/"), Description: ns.Description}

		for _, name := range ns.Pages {
			src := path.Join(sec.Slug, name)
			if listed[src] {
				return nil, fmt.Errorf("%s: %s is listed twice", navFile, src)
			}
			listed[src] = true

			page, err := loadPage(srcDir, src, sec)
			if err != nil {
				return nil, err
			}
			if prior, dup := byURL[page.URL]; dup {
				return nil, fmt.Errorf("%s and %s both resolve to /%s", prior, src, page.URL)
			}
			byURL[page.URL] = src

			// A page at the section's own URL is that section's authored index.
			if sec.Slug != "" && page.URL == sec.Slug+"/" {
				sec.Index = page
			} else {
				sec.Pages = append(sec.Pages, page)
			}
			if page.IsHome() {
				if site.Home != nil {
					return nil, fmt.Errorf("%s: two home pages (%s and %s)", navFile, site.Home.Src, src)
				}
				page.Template = "home"
				site.Home = page
			}
		}

		// Sections need a landing URL: the sidebar header links to it, and a bare
		// /docs/concepts/ must not 404. Generate a card grid when none is authored.
		if sec.Slug != "" && sec.Index == nil {
			sec.Index = &Page{
				Section:     sec,
				URL:         sec.Slug + "/",
				Out:         path.Join(sec.Slug, "index.html"),
				Title:       sec.Title,
				Description: sec.Description,
				Template:    "section",
			}
			if prior, dup := byURL[sec.Index.URL]; dup {
				return nil, fmt.Errorf("%s: generated section index for %q collides with %s", navFile, sec.Title, prior)
			}
			byURL[sec.Index.URL] = navFile
		}

		site.Sections = append(site.Sections, sec)
		if sec.Index != nil {
			site.Pages = append(site.Pages, sec.Index)
		}
		site.Pages = append(site.Pages, sec.Pages...)
	}

	if site.Home == nil {
		return nil, fmt.Errorf("%s: no page resolves to the docs root (expected index.md in the root section)", navFile)
	}
	if err := checkUnlisted(srcDir, listed); err != nil {
		return nil, err
	}

	for _, p := range site.Pages {
		depth := strings.Count(p.URL, "/")
		p.DocsRoot = strings.Repeat("../", depth)
		p.Root = strings.Repeat("../", depth+1)
	}
	for i, p := range site.Pages {
		if i > 0 {
			p.Prev = site.Pages[i-1]
		}
		if i < len(site.Pages)-1 {
			p.Next = site.Pages[i+1]
		}
	}
	return site, nil
}

// loadPage reads one authored markdown file into a Page.
func loadPage(srcDir, src string, sec *Section) (*Page, error) {
	full := filepath.Join(srcDir, filepath.FromSlash(src))
	raw, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("%s (listed in %s): %w", src, navFile, err)
	}
	meta, body, err := splitFrontmatter(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src, err)
	}
	url, err := pageURL(src)
	if err != nil {
		return nil, err
	}
	return &Page{
		Section:     sec,
		Src:         src,
		URL:         url,
		Out:         path.Join(url, "index.html"),
		Title:       meta.Title,
		Description: meta.Description,
		Generate:    meta.Generate,
		Tools:       meta.Tools,
		Template:    "page",
		Markdown:    body,
	}, nil
}

// pageURL maps a docs-src path to its directory-style URL, relative to the docs
// root: index.md -> "", quickstart.md -> "quickstart/",
// reference/mcp/index.md -> "reference/mcp/".
func pageURL(src string) (string, error) {
	rest, ok := strings.CutSuffix(src, ".md")
	if !ok {
		return "", fmt.Errorf("%s: pages must be .md files", src)
	}
	if rest == "" {
		return "", fmt.Errorf("%s: empty page name", src)
	}
	if rest == "index" {
		return "", nil // the docs root
	}
	if base, isIndex := strings.CutSuffix(rest, "/index"); isIndex {
		return base + "/", nil // reference/mcp/index.md -> reference/mcp/
	}
	return rest + "/", nil
}

// isPartial reports whether a docs-src entry is an include rather than a page.
// Underscore-prefixed files and directories (docs-src/reference/mcp/_tools/) are
// pulled in by generators, so they are exempt from the every-file-is-in-the-nav
// rule that checkUnlisted enforces.
func isPartial(name string) bool { return strings.HasPrefix(name, "_") }

// checkUnlisted fails the build on any markdown file the manifest does not name.
// Silent omission is the failure this prevents: an authored page that never
// appears in the nav is invisible, and nothing else would catch it.
func checkUnlisted(srcDir string, listed map[string]bool) error {
	var orphans []string
	err := filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if isPartial(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".md") || isPartial(d.Name()) {
			return nil
		}
		rel, relErr := filepath.Rel(srcDir, p)
		if relErr != nil {
			return relErr
		}
		if slash := filepath.ToSlash(rel); !listed[slash] {
			orphans = append(orphans, slash)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", srcDir, err)
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		return fmt.Errorf("%s does not list: %s", navFile, strings.Join(orphans, ", "))
	}
	return nil
}
