package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want [3]int
		ok   bool
	}{
		{"0.3.4", [3]int{0, 3, 4}, true},
		{"v0.3.4", [3]int{0, 3, 4}, true},
		{" 1.20.300 ", [3]int{1, 20, 300}, true},
		{"0.0.0-dev", [3]int{}, false},              // the dev sentinel
		{"0.3.4-SNAPSHOT-3b28e8b", [3]int{}, false}, // goreleaser snapshot
		{"0.3.4+abc1234", [3]int{}, false},          // build metadata
		{"0.3.4-rc1", [3]int{}, false},              // pre-release
		{"0.3", [3]int{}, false},                    // too few fields
		{"1.2.3.4", [3]int{}, false},                // too many fields
		{"1.x.3", [3]int{}, false},                  // non-numeric field
		{"", [3]int{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := parseVersion(tt.in)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCompareReleases(t *testing.T) {
	tests := []struct {
		name    string
		a, b    string
		wantCmp int
		wantOK  bool
	}{
		{"equal", "0.3.4", "0.3.4", 0, true},
		{"older patch", "0.3.3", "0.3.4", -1, true},
		{"newer patch", "0.3.5", "0.3.4", 1, true},
		{"older minor", "0.2.9", "0.3.0", -1, true},
		{"newer major", "1.0.0", "0.9.9", 1, true},
		{"v prefix both", "v0.3.4", "0.3.4", 0, true},
		{"double-digit patch beats single", "0.3.10", "0.3.9", 1, true},
		{"current is dev", "0.0.0-dev", "0.3.4", 0, false},
		{"current is snapshot", "0.3.4-SNAPSHOT-abc", "0.3.4", 0, false},
		{"latest unparseable", "0.3.4", "garbage", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmp, ok := compareReleases(tt.a, tt.b)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantCmp, cmp)
		})
	}
}

func TestLatestReleaseTag(t *testing.T) {
	t.Run("parses tag_name", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))
			_, _ = w.Write([]byte(`{"tag_name":"v0.4.1","name":"0.4.1"}`))
		}))
		defer srv.Close()
		tag, err := latestReleaseTag(srv.URL)
		require.NoError(t, err)
		require.Equal(t, "v0.4.1", tag)
	})

	t.Run("non-200 is an error with a hint", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden) // GitHub rate limit
		}))
		defer srv.Close()
		_, err := latestReleaseTag(srv.URL)
		require.Error(t, err)
		require.Contains(t, err.Error(), "rate limit")
	})

	t.Run("empty tag is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"tag_name":""}`))
		}))
		defer srv.Close()
		_, err := latestReleaseTag(srv.URL)
		require.Error(t, err)
		require.Contains(t, err.Error(), "tag_name")
	})
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
// its output is piped to a shell (see requireHTTPS). srv.Client() trusts the
// throwaway cert; the scheme rules under test are unchanged.
func TestFetchInstaller(t *testing.T) {
	fetch := func(srv *httptest.Server) (string, error) {
		client := srv.Client()
		client.CheckRedirect = httpsOnlyRedirect
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
