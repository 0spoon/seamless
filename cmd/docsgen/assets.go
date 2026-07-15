package main

import (
	"embed"
	"fmt"
	"path/filepath"
)

//go:embed assets
var assetFS embed.FS

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
