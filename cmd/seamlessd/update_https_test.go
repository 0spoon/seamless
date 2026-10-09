package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arctop/seamless/internal/update"
	"github.com/stretchr/testify/require"
)

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
	client.CheckRedirect = update.HTTPSOnlyRedirect

	body, err := fetchInstallerWith(client, secure.URL)
	require.Error(t, err)
	require.Contains(t, err.Error(), "https")
	require.Empty(t, body)
}

// The release reads are held to the same rules: a plain-http base is refused
// before any request, and a redirect off https is a failed fetch.
func TestReleaseSource_RefusesPlainHTTPAndDowngrades(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#!/bin/sh\necho pwned\n"))
	}))
	defer plain.Close()
	s := releaseSource{client: plain.Client(), apiRepo: plain.URL, downloadBase: plain.URL}
	_, err := s.fetchAsset(context.Background(), mustVersion(t, "0.7.3"), "install")
	require.ErrorContains(t, err, "https")

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
	}))
	defer secure.Close()
	client := secure.Client()
	client.CheckRedirect = update.HTTPSOnlyRedirect
	s = releaseSource{client: client, apiRepo: secure.URL, downloadBase: secure.URL}
	body, err := s.fetchAsset(context.Background(), mustVersion(t, "0.7.3"), "install")
	require.Error(t, err)
	require.Nil(t, body)
}
