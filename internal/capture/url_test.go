package capture

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// fetcherFor builds a URLFetcher using the test server's own client, bypassing
// the SSRF dialer (httptest binds loopback, which the real dialer rejects). This
// exercises the fetch + parse path; SSRF rejection is tested separately.
func fetcherFor(srv *httptest.Server) *URLFetcher {
	return &URLFetcher{client: srv.Client()}
}

func TestFetchURL_ExtractsTitleAndContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, userAgent, r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Hello Title</title></head>
			<body><nav>skip me</nav><article><h1>Heading</h1><p>Main paragraph.</p>
			<script>ignored()</script></article></body></html>`))
	}))
	defer srv.Close()

	c, err := fetcherFor(srv).FetchURL(context.Background(), srv.URL)
	require.NoError(t, err)
	require.Equal(t, "Hello Title", c.Title)
	require.Contains(t, c.Body, "Heading")
	require.Contains(t, c.Body, "Main paragraph.")
	require.NotContains(t, c.Body, "skip me")
	require.NotContains(t, c.Body, "ignored")
}

func TestFetchURL_OGTitleFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><meta property="og:title" content="OG Title"></head>
			<body><main><p>content</p></main></body></html>`))
	}))
	defer srv.Close()
	c, err := fetcherFor(srv).FetchURL(context.Background(), srv.URL)
	require.NoError(t, err)
	require.Equal(t, "OG Title", c.Title)
}

func TestFetchURL_RejectsBadSchemeAndHost(t *testing.T) {
	f := NewURLFetcher()
	_, err := f.FetchURL(context.Background(), "file:///etc/passwd")
	require.ErrorIs(t, err, ErrUnsafeScheme)
	_, err = f.FetchURL(context.Background(), "http://")
	require.ErrorIs(t, err, ErrInvalidURL)
}

func TestFetchURL_RejectsLoopback(t *testing.T) {
	// A real fetcher must refuse to connect to a loopback address (SSRF guard).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, err := NewURLFetcher().FetchURL(context.Background(), srv.URL)
	require.ErrorIs(t, err, ErrPrivateIP)
}

func TestFetchURL_Non2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := fetcherFor(srv).FetchURL(context.Background(), srv.URL)
	require.ErrorIs(t, err, ErrFetchFailed)
}

func TestIsPrivateIP(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1": true, "10.1.2.3": true, "192.168.1.1": true,
		"169.254.1.1": true, "0.0.0.0": true, "::1": true,
		"8.8.8.8": false, "1.1.1.1": false,
	}
	for s, want := range cases {
		require.Equal(t, want, isPrivateIP(net.ParseIP(s)), s)
	}
}
