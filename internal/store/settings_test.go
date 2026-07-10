package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSettingsGetSet(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	_, found, err := GetSetting(ctx, db, "missing")
	require.NoError(t, err)
	require.False(t, found)

	require.NoError(t, SetSetting(ctx, db, "k", "v1"))
	require.NoError(t, SetSetting(ctx, db, "k", "v2")) // upsert
	v, found, err := GetSetting(ctx, db, "k")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "v2", v)
}

func TestResolveProjectForCWD(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Unconfigured map resolves everything to the global scope.
	slug, err := ResolveProjectForCWD(ctx, db, "/Users/x/repos/seamless")
	require.NoError(t, err)
	require.Equal(t, "", slug)

	require.NoError(t, SetSetting(ctx, db, SettingRepoProjectMap,
		`{"/Users/x/repos/seamless":"seamless","/Users/x/repos/seam":"seam"}`))

	slug, err = ResolveProjectForCWD(ctx, db, "/Users/x/repos/seamless/internal/mcp")
	require.NoError(t, err)
	require.Equal(t, "seamless", slug)

	// A sibling that shares a string prefix but not a path boundary must not match.
	slug, err = ResolveProjectForCWD(ctx, db, "/Users/x/repos/seamless-old")
	require.NoError(t, err)
	require.Equal(t, "", slug)

	// Exact directory resolves.
	slug, err = ResolveProjectForCWD(ctx, db, "/Users/x/repos/seam")
	require.NoError(t, err)
	require.Equal(t, "seam", slug)
}

func TestMatchProjectPathLongestPrefix(t *testing.T) {
	m := map[string]string{
		"/a":         "outer",
		"/a/b/inner": "inner",
	}
	require.Equal(t, "inner", matchProjectPath("/a/b/inner/x", m))
	require.Equal(t, "outer", matchProjectPath("/a/b/other", m))
	require.Equal(t, "", matchProjectPath("/z", m))
}
