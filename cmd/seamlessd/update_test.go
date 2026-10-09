package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arctop/seamless/internal/update"
	"github.com/stretchr/testify/require"
)

func TestUpdatePlanFor(t *testing.T) {
	const (
		unixScript = releaseDownloadBase + "/install"
		winScript  = releaseDownloadBase + "/install.ps1"
	)
	tests := []struct {
		name       string
		goos       string
		wantURL    string
		wantBundle string
		wantProg   string
		wantArgs   []string
		wantHint   string
	}{
		{"darwin", "darwin", unixScript, unixScript + ".sigstore.json", "sh", []string{"-s"},
			"curl -fsSL " + unixScript + " | sh"},
		{"linux", "linux", unixScript, unixScript + ".sigstore.json", "sh", []string{"-s"},
			"curl -fsSL " + unixScript + " | sh"},
		{"windows", "windows", winScript, winScript + ".sigstore.json", "powershell",
			[]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "-"},
			"irm " + winScript + " | iex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := updatePlanFor(tt.goos)
			require.Equal(t, tt.goos, p.OS)
			require.Equal(t, tt.wantURL, p.URL)
			require.Equal(t, tt.wantBundle, p.BundleURL)
			require.Equal(t, tt.wantProg, p.Prog)
			require.Equal(t, tt.wantArgs, p.ProgArgs)
			require.Equal(t, tt.wantHint, p.RunHint)
		})
	}
}

// releaseListJSON is a release list as GitHub returns it, newest-created
// first: a backport (v0.5.9) cut after v0.6.0, plus a prerelease that must not
// count.
const releaseListJSON = `[
  {"tag_name":"v0.5.9","published_at":"2026-10-09T12:00:00Z","assets":[
    {"name":"checksums.txt","state":"uploaded"},{"name":"install","state":"uploaded"},
    {"name":"install.sigstore.json","state":"uploaded"}]},
  {"tag_name":"v0.7.0-rc1","prerelease":true,"published_at":"2026-10-09T13:00:00Z","assets":[
    {"name":"checksums.txt","state":"uploaded"},{"name":"install","state":"uploaded"},
    {"name":"install.sigstore.json","state":"uploaded"}]},
  {"tag_name":"v0.6.0","published_at":"2026-10-08T12:00:00Z","assets":[
    {"name":"checksums.txt","state":"uploaded"},{"name":"install","state":"uploaded"},
    {"name":"install.sigstore.json","state":"uploaded"}]}
]`

func TestReportUpdateCheck(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "seamlessd-update-check", r.Header.Get("User-Agent"))
		_, _ = w.Write([]byte(releaseListJSON))
	}))
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = update.HTTPSOnlyRedirect
	f := &update.Fetcher{Client: client, URL: srv.URL, Now: time.Now}

	tests := []struct {
		name    string
		current string
		want    string
	}{
		{"older release", "0.5.4", "update available"},
		{"the newest by version, not the backport", "0.6.0", "up to date"},
		{"the backport is not newer", "0.5.9", "update available"},
		{"ahead", "0.6.1", "ahead of the newest published release"},
		{"dev build", "0.0.0-dev", "development build"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, reportUpdateCheck(context.Background(), &buf, f, "linux", tt.current, tt.current+"+abc1234", nil))
			out := buf.String()
			require.Contains(t, out, tt.want)
			require.Contains(t, out, "v0.6.0", "newest is the max version, never the prerelease")
			require.NotContains(t, out, "0.7.0")
			require.Contains(t, out, tt.current+"+abc1234")
		})
	}
}

func TestReportUpdateCheck_NoInstallableRelease(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(releaseListJSON))
	}))
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = update.HTTPSOnlyRedirect
	f := &update.Fetcher{Client: client, URL: srv.URL, Now: time.Now}

	var buf bytes.Buffer
	// None of the releases carries the Windows installer pair.
	require.NoError(t, reportUpdateCheck(context.Background(), &buf, f, "windows", "0.6.0", "0.6.0", nil))
	require.Contains(t, buf.String(), "no installable release for windows")
}

func TestReportUpdateCheck_RateLimitHint(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = update.HTTPSOnlyRedirect
	f := &update.Fetcher{Client: client, URL: srv.URL, Now: time.Now}

	err := reportUpdateCheck(context.Background(), io.Discard, f, "linux", "0.6.0", "0.6.0", nil)
	require.ErrorIs(t, err, update.ErrRateLimited)
	require.ErrorContains(t, err, "SEAMLESS_VERSION")
}

func TestMissingAssetHint(t *testing.T) {
	t.Run("404 gains the predates-signed-assets hint", func(t *testing.T) {
		orig := &fetchStatusError{url: "u", status: "404 Not Found", code: http.StatusNotFound}
		err := missingAssetHint(orig)
		require.ErrorIs(t, err, orig)
		require.Contains(t, err.Error(), "predates signed installer assets")
	})
	t.Run("other statuses pass through untouched", func(t *testing.T) {
		orig := &fetchStatusError{url: "u", status: "500 Internal Server Error", code: http.StatusInternalServerError}
		err := missingAssetHint(orig)
		require.ErrorIs(t, err, orig)
		require.NotContains(t, err.Error(), "predates")
	})
	t.Run("non-status errors pass through untouched", func(t *testing.T) {
		orig := errTest
		require.ErrorIs(t, missingAssetHint(orig), orig)
	})
}

var errTest = errors.New("transport exploded")

// These use TLS servers because fetchInstaller refuses plain http outright --
// its output is piped to a shell (see update.RequireHTTPS). srv.Client() trusts the
// throwaway cert; the scheme rules under test are unchanged.
func TestFetchInstaller(t *testing.T) {
	fetch := func(srv *httptest.Server) (string, error) {
		client := srv.Client()
		client.CheckRedirect = update.HTTPSOnlyRedirect
		return fetchInstallerWith(client, srv.URL)
	}

	t.Run("returns the script body", func(t *testing.T) {
		const script = "#!/bin/sh\necho hi\n"
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(script))
		}))
		defer srv.Close()
		got, err := fetch(srv)
		require.NoError(t, err)
		require.Equal(t, script, got)
	})

	t.Run("non-200 is an error", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()
		_, err := fetch(srv)
		require.Error(t, err)
		require.Contains(t, err.Error(), "404")
	})

	t.Run("empty response is an error", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("   \n"))
		}))
		defer srv.Close()
		_, err := fetch(srv)
		require.Error(t, err)
		require.Contains(t, err.Error(), "empty")
	})
}
