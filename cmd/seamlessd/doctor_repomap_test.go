package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/0spoon/seamless/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRepoMapCheck(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	// An empty map is the fresh-install state, not a problem.
	c := repoMapCheck(db)
	require.Equal(t, statusOK, c.status)
	require.Contains(t, c.detail, "no repos mapped")

	// Every mapped path present on disk: quiet ok with the count.
	present := t.TempDir()
	require.NoError(t, store.AddRepoMapping(ctx, db, present, "here"))
	c = repoMapCheck(db)
	require.Equal(t, statusOK, c.status)
	require.Contains(t, c.detail, "1 mapped paths, all present")

	// A dangling entry warns, names the path and slug, and points at map-repo
	// for the moved-and-renamed case the auto-adoption cannot recognize.
	missing := filepath.Join(t.TempDir(), "gone")
	require.NoError(t, store.AddRepoMapping(ctx, db, missing, "gone-project"))
	c = repoMapCheck(db)
	require.Equal(t, statusWarn, c.status)
	require.Contains(t, c.detail, "1 of 2 mapped paths missing")
	require.Contains(t, c.detail, missing)
	require.Contains(t, c.detail, "gone-project")
	require.Contains(t, c.detail, "map-repo")
}
