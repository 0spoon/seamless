package update

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// posixAssets and windowsAssets are the uploaded asset sets a complete release
// carries for each installer family (requiredAssets plus an archive, which the
// filter does not need).
var (
	posixAssets = []APIAsset{
		{Name: "seamless_0.7.3_darwin_arm64.tar.gz", State: "uploaded"},
		{Name: "checksums.txt", State: "uploaded"},
		{Name: "install", State: "uploaded"},
		{Name: "install.sigstore.json", State: "uploaded"},
	}
	windowsAssets = []APIAsset{
		{Name: "seamless_0.7.3_windows_amd64.zip", State: "uploaded"},
		{Name: "checksums.txt", State: "uploaded"},
		{Name: "install.ps1", State: "uploaded"},
		{Name: "install.ps1.sigstore.json", State: "uploaded"},
	}
)

func at(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func allAssets() []APIAsset { return append(append([]APIAsset{}, posixAssets...), windowsAssets...) }

func versions(rels []Release) []string {
	out := make([]string, 0, len(rels))
	for _, r := range rels {
		out = append(out, r.Version.String())
	}
	return out
}

func TestFilter(t *testing.T) {
	published := at("2026-10-09T10:00:00Z")
	tests := []struct {
		name string
		rel  APIRelease
		goos string
		keep bool
	}{
		{"complete posix release", APIRelease{TagName: "v0.7.3", PublishedAt: published, Assets: posixAssets}, "darwin", true},
		{"complete posix release on linux", APIRelease{TagName: "v0.7.3", PublishedAt: published, Assets: posixAssets}, "linux", true},
		{"windows needs the ps1 pair", APIRelease{TagName: "v0.7.3", PublishedAt: published, Assets: posixAssets}, "windows", false},
		{"complete windows release", APIRelease{TagName: "v0.7.3", PublishedAt: published, Assets: windowsAssets}, "windows", true},
		{"draft", APIRelease{TagName: "v0.7.3", Draft: true, PublishedAt: published, Assets: allAssets()}, "linux", false},
		{"prerelease flag (the yank lever)", APIRelease{TagName: "v0.7.3", Prerelease: true, PublishedAt: published, Assets: allAssets()}, "linux", false},
		{"rc tag", APIRelease{TagName: "v0.7.3-rc1", PublishedAt: published, Assets: allAssets()}, "linux", false},
		{"tag without v", APIRelease{TagName: "0.7.3", PublishedAt: published, Assets: allAssets()}, "linux", false},
		{"no published_at", APIRelease{TagName: "v0.7.3", Assets: allAssets()}, "linux", false},
		{"bundle still uploading", APIRelease{TagName: "v0.7.3", PublishedAt: published, Assets: []APIAsset{
			{Name: "checksums.txt", State: "uploaded"},
			{Name: "install", State: "uploaded"},
			{Name: "install.sigstore.json", State: "open"},
		}}, "linux", false},
		{"bundle missing", APIRelease{TagName: "v0.7.3", PublishedAt: published, Assets: []APIAsset{
			{Name: "checksums.txt", State: "uploaded"},
			{Name: "install", State: "uploaded"},
		}}, "linux", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Filter([]APIRelease{tt.rel}, tt.goos)
			if !tt.keep {
				require.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			require.Equal(t, Version{0, 7, 3}, got[0].Version)
			require.Equal(t, published.UTC(), got[0].PublishedAt)
		})
	}
}

// TestFilter_OrdersByVersionNotListOrder: GitHub lists releases by creation, so
// a backport cut after a newer release comes first in the list. Filter and
// Newest go by version.
func TestFilter_OrdersByVersionNotListOrder(t *testing.T) {
	list := []APIRelease{
		{TagName: "v0.5.9", PublishedAt: at("2026-10-09T12:00:00Z"), Assets: allAssets()}, // backport, created last
		{TagName: "v0.6.0", PublishedAt: at("2026-10-08T12:00:00Z"), Assets: allAssets()},
		{TagName: "v0.6.1-rc1", PublishedAt: at("2026-10-09T13:00:00Z"), Assets: allAssets()},
		{TagName: "v0.5.10", PublishedAt: at("2026-10-01T12:00:00Z"), Assets: allAssets()},
	}
	got := Filter(list, "linux")
	require.Equal(t, []string{"0.6.0", "0.5.10", "0.5.9"}, versions(got))

	newest, ok := Newest(got)
	require.True(t, ok)
	require.Equal(t, "0.6.0", newest.Version.String())

	// Newest does not depend on its input being sorted.
	shuffled := []Release{got[2], got[0], got[1]}
	newest, ok = Newest(shuffled)
	require.True(t, ok)
	require.Equal(t, "0.6.0", newest.Version.String())
}

func TestFilter_DuplicateVersionKeepsEarliest(t *testing.T) {
	list := []APIRelease{
		{TagName: "v0.7.3", PublishedAt: at("2026-10-09T12:00:00Z"), Assets: allAssets()},
		{TagName: "v0.7.3", PublishedAt: at("2026-10-09T10:00:00Z"), Assets: allAssets()},
	}
	got := Filter(list, "darwin")
	require.Len(t, got, 1)
	require.Equal(t, at("2026-10-09T10:00:00Z").UTC(), got[0].PublishedAt)
}

func TestNewest_Empty(t *testing.T) {
	_, ok := Newest(nil)
	require.False(t, ok)
}

func TestReleaseURL(t *testing.T) {
	require.Equal(t, "https://github.com/arctop/seamless/releases/tag/v0.7.3", ReleaseURL(Version{0, 7, 3}))
}
