package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arctop/seamless/internal/update"
)

// The archive name is goreleaser's name_template and the installers' own
// spelling, per platform.
func TestPlatformArchive(t *testing.T) {
	v := mustVersion(t, "0.7.3")
	require.Equal(t, "seamless_0.7.3_darwin_arm64.tar.gz", platformArchive(v, "darwin", "arm64"))
	require.Equal(t, "seamless_0.7.3_linux_amd64.tar.gz", platformArchive(v, "linux", "amd64"))
	require.Equal(t, "seamless_0.7.3_windows_amd64.zip", platformArchive(v, "windows", "amd64"))
	require.Equal(t, "install", installerAsset("linux"))
	require.Equal(t, "install.ps1", installerAsset("windows"))
	require.Equal(t, []string{"install.ps1", "install.ps1.sigstore.json", "checksums.txt", "checksums.txt.sigstore.json",
		"seamless_0.7.3_windows_arm64.zip"}, releaseAssetNames(v, "windows", "arm64"))
}

func TestArchiveChecksum(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	const name = "seamless_0.7.3_linux_amd64.tar.gz"
	tests := []struct {
		name      string
		checksums string
		ok        bool
	}{
		{"exactly one line", sum + "  " + name + "\n" + sum + "  seamless_0.7.3_linux_arm64.tar.gz\n", true},
		{"upper-case hex", strings.ToUpper(sum) + "  " + name + "\n", true},
		{"not listed", sum + "  seamless_0.7.3_linux_arm64.tar.gz\n", false},
		{"listed twice", sum + "  " + name + "\n" + sum + "  " + name + "\n", false},
		{"a longer name is not this one", sum + "  " + name + ".sig\n", false},
		{"a malformed checksum", "xyz  " + name + "\n", false},
		{"a short checksum", "abcd  " + name + "\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := archiveChecksum([]byte(tt.checksums), name)
			if !tt.ok {
				require.ErrorIs(t, err, errChecksumsArchive)
				return
			}
			require.NoError(t, err)
			require.Equal(t, sum, got)
		})
	}
}

func TestCheckPublished(t *testing.T) {
	v := mustVersion(t, "0.7.3")
	published := time.Now().Add(-time.Hour)
	need := []string{"install", "install.sigstore.json"}
	full := update.APIRelease{TagName: "v0.7.3", PublishedAt: &published, Assets: []update.APIAsset{
		{Name: "install", State: "uploaded"}, {Name: "install.sigstore.json", State: "uploaded"},
	}}
	require.NoError(t, checkPublished(full, v, need))

	tests := []struct {
		name string
		edit func(r *update.APIRelease)
		want error
	}{
		{"a prerelease is the yank", func(r *update.APIRelease) { r.Prerelease = true }, errReleasePulled},
		{"a draft", func(r *update.APIRelease) { r.Draft = true }, errReleasePulled},
		{"unpublished", func(r *update.APIRelease) { r.PublishedAt = nil }, errReleasePulled},
		{"another tag", func(r *update.APIRelease) { r.TagName = "v0.7.30" }, errReleasePulled},
		{"an odd spelling of the tag", func(r *update.APIRelease) { r.TagName = "0.7.3" }, errReleasePulled},
		{"an asset still uploading", func(r *update.APIRelease) { r.Assets[1].State = "open" }, errReleaseInFlight},
		{"an asset missing", func(r *update.APIRelease) { r.Assets = r.Assets[:1] }, errReleaseIncomplete},
		{"uploading wins over missing: the release is still being assembled", func(r *update.APIRelease) {
			r.Assets = []update.APIAsset{{Name: "install", State: "open"}}
		}, errReleaseInFlight},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel := full
			rel.Assets = append([]update.APIAsset(nil), full.Assets...)
			tt.edit(&rel)
			err := checkPublished(rel, v, need)
			require.ErrorIs(t, err, tt.want)
			require.NotContains(t, err.Error(), "0.7.30", "release text never reaches a message")
		})
	}
}

// A failed read is the network's (back off) or the release's (it is not
// there), and the updater records the two at different stages.
func TestReleaseSource_ClassifiesFailures(t *testing.T) {
	var (
		mu     sync.Mutex
		status = http.StatusOK
		body   = `{"tag_name":"v0.7.3","published_at":"2026-10-08T12:00:00Z"}`
	)
	set := func(st int, b string) {
		mu.Lock()
		defer mu.Unlock()
		status, body = st, b
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, update.UserAgent, r.Header.Get("User-Agent"))
		assert.Empty(t, r.Header.Get("Authorization"), "never a token")
		if strings.Contains(r.URL.Path, "/releases/tags/") {
			assert.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))
		}
		mu.Lock()
		st, b := status, body
		mu.Unlock()
		w.WriteHeader(st)
		_, _ = w.Write([]byte(b))
	}))
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = update.HTTPSOnlyRedirect
	s := releaseSource{client: client, apiRepo: srv.URL + "/repos/arctop/seamless", downloadBase: srv.URL + "/releases/download"}
	v := mustVersion(t, "0.7.3")
	ctx := context.Background()

	rel, err := s.releaseByTag(ctx, v)
	require.NoError(t, err)
	require.Equal(t, "v0.7.3", rel.TagName)

	for _, tt := range []struct {
		status int
		class  error
		stage  update.Stage
	}{
		{http.StatusNotFound, errFetchMissing, update.StageVerify},
		{http.StatusGone, errFetchMissing, update.StageVerify},
		{http.StatusTooManyRequests, errFetchNetwork, update.StageFetch},
		{http.StatusForbidden, errFetchNetwork, update.StageFetch},
		{http.StatusInternalServerError, errFetchNetwork, update.StageFetch},
		{http.StatusBadGateway, errFetchNetwork, update.StageFetch},
	} {
		set(tt.status, "")
		_, err := s.releaseByTag(ctx, v)
		require.ErrorIs(t, err, tt.class, "status %d", tt.status)
		require.Equal(t, tt.stage, fetchFailureStage(err))
		_, err = s.fetchAsset(ctx, v, "install")
		require.ErrorIs(t, err, tt.class, "status %d", tt.status)
		require.ErrorIs(t, s.headAsset(ctx, v, "install"), tt.class)
	}

	set(http.StatusOK, "   \n")
	_, err = s.fetchAsset(ctx, v, "install")
	require.ErrorIs(t, err, errFetchNetwork, "an empty reply is a misrouted one")
	set(http.StatusOK, strings.Repeat("x", maxAssetBytes+1))
	_, err = s.fetchAsset(ctx, v, "install")
	require.ErrorIs(t, err, errFetchNetwork, "an oversized reply is a misrouted one")
	set(http.StatusOK, "{not json")
	_, err = s.releaseByTag(ctx, v)
	require.ErrorIs(t, err, errFetchNetwork)

	srv.Close()
	_, err = s.fetchAsset(ctx, v, "install")
	require.ErrorIs(t, err, errFetchNetwork, "a transport failure")
	require.Equal(t, update.StageFetch, fetchFailureStage(err))
}

func TestFetchFailureStage(t *testing.T) {
	require.Equal(t, update.StageVerify, fetchFailureStage(errReleasePulled))
	require.Equal(t, update.StageVerify, fetchFailureStage(errReleaseIncomplete))
	require.Equal(t, update.StageVerify, fetchFailureStage(&fetchError{url: "u", class: errFetchMissing, detail: "404"}))
	require.Equal(t, update.StageFetch, fetchFailureStage(errReleaseInFlight))
	require.Equal(t, update.StageFetch, fetchFailureStage(&fetchError{url: "u", class: errFetchNetwork, detail: "502"}))
	// A 404 for an asset the re-read listed as uploaded is in flight.
	gone := listedAssetGone(&fetchError{url: "u", class: errFetchMissing, detail: "404"})
	require.ErrorIs(t, gone, errReleaseInFlight)
	require.Equal(t, update.StageFetch, fetchFailureStage(gone))
	other := &fetchError{url: "u", class: errFetchNetwork, detail: "502"}
	require.Equal(t, error(other), listedAssetGone(other))
}
