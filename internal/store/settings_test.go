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

func TestSiblingProjects(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// No family configured -> no siblings.
	sibs, err := SiblingProjects(ctx, db, "app")
	require.NoError(t, err)
	require.Empty(t, sibs)

	require.NoError(t, SetSetting(ctx, db, SettingProjectFamilies,
		`{"product":["app","backend","agent"],"infra":["app","ops"]}`))

	// app appears in two families; siblings are the union, app excluded, deduped.
	sibs, err = SiblingProjects(ctx, db, "app")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"backend", "agent", "ops"}, sibs)

	// A project with no family membership has no siblings.
	sibs, err = SiblingProjects(ctx, db, "lonely")
	require.NoError(t, err)
	require.Empty(t, sibs)

	// The global scope never has siblings.
	sibs, err = SiblingProjects(ctx, db, "")
	require.NoError(t, err)
	require.Empty(t, sibs)
}

func TestProjectFamilyMutators(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Add to a brand-new family.
	members, err := AddFamilyMembers(ctx, db, "hegemon", []string{"app", "backend"})
	require.NoError(t, err)
	require.Equal(t, []string{"app", "backend"}, members)

	// Adding again unions and dedupes, preserving first-seen order; whitespace is
	// trimmed and blanks dropped.
	members, err = AddFamilyMembers(ctx, db, "hegemon", []string{" backend ", "agent", "app", ""})
	require.NoError(t, err)
	require.Equal(t, []string{"app", "backend", "agent"}, members)

	// It round-trips through the read path used by briefings.
	fams, err := ProjectFamilies(ctx, db)
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"hegemon": {"app", "backend", "agent"}}, fams)

	// Removing a subset keeps the rest, in order.
	members, err = RemoveFamilyMembers(ctx, db, "hegemon", []string{"agent"})
	require.NoError(t, err)
	require.Equal(t, []string{"app", "backend"}, members)

	// Removing an unknown family errors with the sentinel.
	_, err = RemoveFamilyMembers(ctx, db, "nope", nil)
	require.ErrorIs(t, err, ErrFamilyNotFound)

	// Removing the remaining members empties and drops the family.
	members, err = RemoveFamilyMembers(ctx, db, "hegemon", []string{"app", "backend"})
	require.NoError(t, err)
	require.Empty(t, members)
	fams, err = ProjectFamilies(ctx, db)
	require.NoError(t, err)
	require.Empty(t, fams)

	// Removing a whole family by name (no slugs) also drops it.
	_, err = AddFamilyMembers(ctx, db, "x", []string{"a"})
	require.NoError(t, err)
	_, err = RemoveFamilyMembers(ctx, db, "x", nil)
	require.NoError(t, err)
	fams, err = ProjectFamilies(ctx, db)
	require.NoError(t, err)
	require.Empty(t, fams)
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
