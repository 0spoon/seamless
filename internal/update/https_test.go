package update

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequireHTTPS(t *testing.T) {
	require.NoError(t, RequireHTTPS("https://thereisnospoon.org/install"))
	require.NoError(t, RequireHTTPS("HTTPS://thereisnospoon.org/install"))

	for _, bad := range []string{
		"http://thereisnospoon.org/install",
		"file:///tmp/evil.sh",
		"ftp://example.com/install",
		"//thereisnospoon.org/install", // scheme-relative: no scheme at all
	} {
		err := RequireHTTPS(bad)
		require.Error(t, err, "must refuse %s", bad)
		require.Contains(t, err.Error(), "https")
	}
}

func TestHTTPSOnlyRedirect(t *testing.T) {
	req := func(raw string) *http.Request {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		return &http.Request{URL: u}
	}
	require.NoError(t, HTTPSOnlyRedirect(req("https://example.com/next"), nil))
	require.Error(t, HTTPSOnlyRedirect(req("http://example.com/next"), nil))

	via := make([]*http.Request, 10)
	require.ErrorContains(t, HTTPSOnlyRedirect(req("https://example.com/next"), via), "10 redirects")
}
