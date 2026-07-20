package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequireHTTPS(t *testing.T) {
	require.NoError(t, requireHTTPS("https://thereisnospoon.org/install"))
	require.NoError(t, requireHTTPS("HTTPS://thereisnospoon.org/install"))

	for _, bad := range []string{
		"http://thereisnospoon.org/install",
		"file:///tmp/evil.sh",
		"ftp://example.com/install",
		"//thereisnospoon.org/install", // scheme-relative: no scheme at all
	} {
		require.Error(t, requireHTTPS(bad), "must refuse %s", bad)
	}
}

// fetchInstaller feeds a shell, so it must refuse a plain-http --url outright
// rather than trusting the operator typed it deliberately.
func TestFetchInstaller_RefusesPlainHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#!/bin/sh\necho pwned\n"))
	}))
	defer srv.Close()

	_, err := fetchInstaller(srv.URL) // httptest.NewServer is http://
	require.Error(t, err)
	require.Contains(t, err.Error(), "https")
}

// The scheme check on the first URL is worthless if a redirect can walk it back
// down to plaintext, which Go's default client follows without complaint.
func TestFetchInstaller_RefusesHTTPSToHTTPDowngrade(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#!/bin/sh\necho pwned\n"))
	}))
	defer plain.Close()

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer secure.Close()

	// The server's own client trusts its throwaway cert, so TLS verification
	// cannot be what fails here -- the redirect guard has to be.
	client := secure.Client()
	client.CheckRedirect = httpsOnlyRedirect

	body, err := fetchInstallerWith(client, secure.URL)
	require.Error(t, err)
	require.Contains(t, err.Error(), "https")
	require.Empty(t, body)
}
