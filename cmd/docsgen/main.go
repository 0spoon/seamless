// Command docsgen renders the Seamless documentation site: markdown authored in
// docs-src/ becomes static HTML committed under docs/docs/, served by the same
// GitHub Pages config as the landing page (thereisnospoon.org/docs/).
//
// The generated tree is committed, not built in CI, so `make docs-check` can
// prove the checked-in output matches the sources. That only works if rendering
// is deterministic: no timestamps, no map iteration order, no concurrency. See
// determinism_test.go, which renders twice and demands byte-equal output.
//
//	docsgen -src docs-src -out docs/docs      # regenerate (what `make docs` runs)
//	docsgen -serve 127.0.0.1:8899             # regenerate, then serve docs/ locally
//
// Every page is written twice: index.html, and a markdown twin at index.md
// holding the page's full source markdown (see twin.go); the site root gets an
// index.md too (the llms.txt outline). The twins exist for
// `Accept: text/markdown` content negotiation -- a Cloudflare Transform Rule on
// the zone rewrites markdown-accepting requests for negotiable directory URLs
// to their index.md; browsers keep getting the HTML. SITE.md documents the rule.
//
// Besides the docs tree, every run refreshes the crawler files at the site root
// (-site, default docs/): sitemap.xml, naming the landing page and every docs
// page; robots.txt, which points crawlers at it; and llms.txt / llms-full.txt,
// the site's nav and full source markdown for LLM consumers. All are committed
// and gated by `make docs-check`, so none can go stale against the nav.
//
// Two pages are generated rather than authored, via a `generate:` key in their
// frontmatter (see generators.go): the MCP tool reference reads mcp.Catalog(),
// and the configuration reference reflects over config.Defaults(). Both derive
// from the code they document, so neither can drift from it silently.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	src := flag.String("src", "docs-src", "directory of authored markdown sources")
	out := flag.String("out", filepath.Join("docs", "docs"), "output directory for generated HTML (contents are replaced)")
	site := flag.String("site", "docs", "site root receiving the crawler files (files are overwritten, nothing is deleted)")
	serve := flag.String("serve", "", "after generating, serve the site root on this address (e.g. 127.0.0.1:8899)")
	flag.Parse()

	if err := run(*src, *out, *site, *serve); err != nil {
		fmt.Fprintln(os.Stderr, "docsgen:", err)
		os.Exit(1)
	}
}

func run(src, out, siteDir, serveAddr string) error {
	// The generators read repo files by relative path (seamless.yaml.example),
	// and agent shells do not reliably inherit a cwd. Fail with a clear message
	// rather than a confusing "no such file" three layers down.
	if err := requireRepoRoot(); err != nil {
		return err
	}

	site, err := loadSite(src)
	if err != nil {
		return err
	}
	scenes, err := loadScenes(scenesPath)
	if err != nil {
		return err
	}
	site.Scenarios, err = loadScenarios(src, scenes)
	if err != nil {
		return err
	}
	if err := renderPages(site); err != nil {
		return err
	}
	if err := renderScenarios(site); err != nil {
		return err
	}
	if err := writeSite(out, site); err != nil {
		return err
	}
	if err := writeScenarios(siteDir, site); err != nil {
		return err
	}
	if err := writeSiteRoot(siteDir, site); err != nil {
		return err
	}
	fmt.Printf("docsgen: wrote %d pages to %s, %d scenario pages and the crawler files to %s\n",
		len(site.Pages), out, len(site.Scenarios), siteDir)

	if serveAddr != "" {
		// The docs live at <root>/docs/, so serve the parent: the same relative
		// hrefs then resolve here and on the deployed site.
		return serveSite(serveAddr, filepath.Dir(out))
	}
	return nil
}

// requireRepoRoot verifies the process is running from the repository root, the
// only cwd from which the -src, -out, and generator input paths resolve.
func requireRepoRoot() error {
	const marker = "go.mod"
	if _, err := os.Stat(marker); err != nil {
		wd, wdErr := os.Getwd()
		if wdErr != nil {
			wd = "the working directory"
		}
		return fmt.Errorf("must run from the repository root (no %s in %s); try `make docs`", marker, wd)
	}
	return nil
}
