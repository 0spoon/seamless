package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed assets
var assetFS embed.FS

// landingCSSPath is the shared stylesheet the docs pages link from the site root
// via their Root prefix. writeAssets does not emit it (the landing page owns it),
// but docsgen must hash it to cache-bust the reference. The path is repo-root
// relative, which is docsgen's guaranteed cwd (requireRepoRoot), so `make docs`
// and `make docs-check` -- which render into different out dirs -- read the same
// file and stamp the same token the landing page's `make site-stamp` does.
const landingCSSPath = "docs/static/site.css"

// assetVersions holds the "?v=<hash>" cache-buster suffix for each mutable asset
// the layout links. The docs pages sit behind the same CDN edge cache as the
// landing page, which caches static/ for hours while passing HTML through, so a
// deploy that changes a stylesheet or script serves it stale behind fresh HTML
// until the TTL lapses (see scripts/site-stamp.sh). A per-content ?v= makes a
// changed asset a new URL the edge never cached.
type assetVersions struct {
	SiteCSS string // shared landing stylesheet (via Root)
	DocsCSS string // docs-only stylesheet (via DocsRoot)
	DocsJS  string // docs-only script (via DocsRoot)
}

// assetVersion returns "?v=<first 8 hex of sha256>" for content, or "" when
// content is empty. The 8-hex form matches scripts/site-stamp.sh, so a shared
// asset carries the same token on the docs pages and the landing page.
func assetVersion(content []byte) string {
	if len(content) == 0 {
		return ""
	}
	sum := sha256.Sum256(content)
	return "?v=" + hex.EncodeToString(sum[:])[:8]
}

// loadAssetVersions hashes the three mutable assets the layout links: the two
// docs-only assets from the embedded FS (the same bytes writeAssets emits) and
// the shared site.css from disk. It is called once per render, from writeSite,
// whose only callers run from the repo root.
func loadAssetVersions() (assetVersions, error) {
	docsCSS, err := assetFS.ReadFile("assets/docs.css")
	if err != nil {
		return assetVersions{}, fmt.Errorf("read embedded docs.css: %w", err)
	}
	docsJS, err := assetFS.ReadFile("assets/docs.js")
	if err != nil {
		return assetVersions{}, fmt.Errorf("read embedded docs.js: %w", err)
	}
	siteCSS, err := os.ReadFile(landingCSSPath)
	if err != nil {
		return assetVersions{}, fmt.Errorf("read %s (docsgen must run from the repo root): %w", landingCSSPath, err)
	}
	return assetVersions{
		SiteCSS: assetVersion(siteCSS),
		DocsCSS: assetVersion(docsCSS),
		DocsJS:  assetVersion(docsJS),
	}, nil
}

// writeAssets copies the docs-only stylesheet and script into <out>/static/.
// The landing page's site.css, fonts, and favicon are NOT copied: they already
// live at the site root and every page links them via its Root prefix, so the
// two surfaces share one design system and one theme cookie rather than drifting
// copies.
func writeAssets(outDir string) error {
	entries, err := assetFS.ReadDir("assets")
	if err != nil {
		return fmt.Errorf("read embedded assets: %w", err)
	}
	// ReadDir returns entries sorted by filename, so this loop is deterministic.
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		content, err := assetFS.ReadFile("assets/" + e.Name())
		if err != nil {
			return fmt.Errorf("read embedded asset %s: %w", e.Name(), err)
		}
		if err := writeFile(filepath.Join(outDir, "static", e.Name()), content); err != nil {
			return err
		}
	}
	return nil
}
