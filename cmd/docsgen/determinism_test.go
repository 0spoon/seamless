package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// renderRepoSite generates the real docs-src into a temp dir and returns every
// output file keyed by its relative path.
func renderRepoSite(t *testing.T) map[string]string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "docs")

	site, err := loadSite("docs-src")
	require.NoError(t, err)
	require.NoError(t, renderPages(site))
	require.NoError(t, writeSite(out, site))

	files := make(map[string]string)
	require.NoError(t, filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(out, p)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(raw)
		return nil
	}))
	return files
}

// TestRenderIsDeterministic is the assumption `make docs-check` rests on: the
// committed tree can only be diffed against a fresh render if two renders of the
// same sources are byte-identical. Map iteration order, a timestamp, or
// concurrent generation would all break the gate with phantom drift.
func TestRenderIsDeterministic(t *testing.T) {
	repoRoot(t)

	first := renderRepoSite(t)
	second := renderRepoSite(t)

	require.NotEmpty(t, first)
	require.Equal(t, len(first), len(second), "both renders emit the same file set")
	for name, body := range first {
		require.Equal(t, body, second[name], "%s differs between two renders of identical sources", name)
	}
}

// TestOutputHasNoBuildTimestamp is the tripwire for the most likely way someone
// breaks determinism later: stamping a build date into the layout ("last updated
// ..."). That would pass review, pass locally, and turn `make docs-check` red on
// the next unrelated PR.
//
// TestRenderIsDeterministic does not cover it: its two renders are milliseconds
// apart, so a second-granularity stamp is identical in both and slips through.
// This looks for *today's* date instead, which is what a build stamp would be by
// definition -- and, unlike a generic timestamp regex, does not fire on the
// memory frontmatter examples the docs legitimately print.
func TestOutputHasNoBuildTimestamp(t *testing.T) {
	repoRoot(t)

	now := time.Now().UTC()
	today := []string{
		now.Format("2006-01-02"),  // 2026-07-15
		now.Format("2 Jan 2006"),  // 15 Jul 2026
		now.Format("Jan 2, 2006"), // Jul 15, 2026
	}
	for name, body := range renderRepoSite(t) {
		if !strings.HasSuffix(name, ".html") {
			continue
		}
		for _, stamp := range today {
			require.NotContains(t, body, stamp,
				"%s contains today's date (%s): a build timestamp makes docs-check drift on its own", name, stamp)
		}
	}
}

// TestEveryPageIsMarked: the generated marker is both a note to a human who
// opens the file and the interlock cleanOutput checks before deleting a tree.
func TestEveryPageIsMarked(t *testing.T) {
	repoRoot(t)

	for name, body := range renderRepoSite(t) {
		if strings.HasSuffix(name, ".html") {
			require.Contains(t, body, generatedMarker, "%s is missing the generated marker", name)
		}
	}
}

// TestCleanOutputRefusesForeignDirectory guards the landing page. `-out docs`
// instead of `-out docs/docs` would otherwise RemoveAll index.html, CNAME, and
// the fonts.
func TestCleanOutputRefusesForeignDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>hand-written landing page</h1>"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CNAME"), []byte("thereisnospoon.org"), 0o644))

	err := cleanOutput(dir)
	require.ErrorContains(t, err, "refusing to replace")
	require.FileExists(t, filepath.Join(dir, "CNAME"), "nothing was deleted")

	// A directory we generated is fair game.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte(generatedMarker+"\n<h1>docs</h1>"), 0o644))
	require.NoError(t, cleanOutput(dir))
	require.NoDirExists(t, dir)
}

// TestCheckLinksCatchesBrokenCrossReferences: a link to a page that does not
// exist still renders as a link, so only a build error catches it before a
// reader clicks it.
func TestCheckLinksCatchesBrokenCrossReferences(t *testing.T) {
	dir := writeSrc(t, map[string]string{
		"nav.yaml": "sections:\n  - title: A\n    slug: \"\"\n    pages: [index.md, real.md]\n",
		"index.md": page("Home", "see [real](/real/), [ghost](/concepts/ghost/) and [anchor](/real/#top)"),
		"real.md":  page("Real", "hi"),
	})
	site, err := loadSite(dir)
	require.NoError(t, err)

	err = renderPages(site)
	require.ErrorContains(t, err, "links to pages that do not exist")
	require.ErrorContains(t, err, "index.md -> /concepts/ghost/")
	require.NotContains(t, err.Error(), "/real/", "existing pages, with or without a fragment, are fine")
}

func TestCleanOutputAcceptsMissingOrEmpty(t *testing.T) {
	require.NoError(t, cleanOutput(filepath.Join(t.TempDir(), "does-not-exist")))
	require.NoError(t, cleanOutput(t.TempDir()))
}

// TestSearchIndexFollowsNavOrder: ties in the client's scoring fall back to
// index order, so it must be the order the sidebar shows.
func TestSearchIndexFollowsNavOrder(t *testing.T) {
	repoRoot(t)

	files := renderRepoSite(t)
	index, ok := files["static/search-index.json"]
	require.True(t, ok, "the search index ships with the site")
	require.Less(t, strings.Index(index, `"url":"quickstart/"`), strings.Index(index, `"url":"reference/"`),
		"index order is nav order")
}
