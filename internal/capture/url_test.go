package capture

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
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
	// Port 80 passes the port allowlist, so the private-IP check is what fires;
	// the dialer rejects before any connection attempt, so no network is touched.
	_, err := NewURLFetcher().FetchURL(context.Background(), "http://127.0.0.1/")
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

// staticLookup returns a lookupIPFunc that resolves every host to the given
// addresses, in order. No real DNS is involved.
func staticLookup(ipStrs ...string) lookupIPFunc {
	return func(_ context.Context, _ string) ([]net.IPAddr, error) {
		addrs := make([]net.IPAddr, len(ipStrs))
		for i, s := range ipStrs {
			addrs[i] = net.IPAddr{IP: net.ParseIP(s)}
		}
		return addrs, nil
	}
}

// recordingDialer returns a dialContextFunc that records every addr it is asked
// to dial and delegates to next. Safe for concurrent use.
func recordingDialer(dialed *[]string, mu *sync.Mutex, next dialContextFunc) dialContextFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		*dialed = append(*dialed, addr)
		mu.Unlock()
		return next(ctx, network, addr)
	}
}

// refuseDial is a dialContextFunc that fails every attempt; tests use it where
// no connection must ever be made.
func refuseDial(_ context.Context, _, addr string) (net.Conn, error) {
	return nil, fmt.Errorf("refused dial to %s", addr)
}

func TestSSRFSafeDialer_PrivateAmongPublicRejected(t *testing.T) {
	// A resolver mixing private records among public ones must fail the whole
	// lookup before any dial (DNS rebinding variant: the private IP must not be
	// reachable no matter where it hides in the answer).
	tests := []struct {
		name string
		ips  []string
	}{
		{"private first", []string{"10.0.0.5", "8.8.8.8"}},
		{"private last", []string{"8.8.8.8", "10.0.0.5"}},
		{"private middle", []string{"8.8.8.8", "169.254.169.254", "1.1.1.1"}},
		{"nat64 mapped loopback last", []string{"8.8.8.8", "64:ff9b::7f00:1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dialed []string
			var mu sync.Mutex
			d := ssrfSafeDialer(staticLookup(tt.ips...), recordingDialer(&dialed, &mu, refuseDial))
			_, err := d(context.Background(), "tcp", "rebind.test:80")
			require.ErrorIs(t, err, ErrPrivateIP)
			require.Empty(t, dialed, "no address may be dialed when any resolved IP is private")
		})
	}
}

func TestSSRFSafeDialer_DialsOnlyValidatedPinnedIPs(t *testing.T) {
	// The dialer must connect to the exact validated IP literals, in resolution
	// order, falling back to the next validated IP when a dial fails.
	var dialed []string
	var mu sync.Mutex
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	dial := recordingDialer(&dialed, &mu, func(_ context.Context, _, addr string) (net.Conn, error) {
		if addr == "8.8.8.8:443" {
			return nil, errors.New("first IP unreachable")
		}
		return client, nil
	})
	d := ssrfSafeDialer(staticLookup("8.8.8.8", "1.1.1.1"), dial)
	conn, err := d(context.Background(), "tcp", "multi.test:443")
	require.NoError(t, err)
	require.Same(t, net.Conn(client), conn)
	require.Equal(t, []string{"8.8.8.8:443", "1.1.1.1:443"}, dialed)
}

func TestSSRFSafeDialer_AllDialsFail(t *testing.T) {
	var dialed []string
	var mu sync.Mutex
	d := ssrfSafeDialer(staticLookup("8.8.8.8", "1.1.1.1"), recordingDialer(&dialed, &mu, refuseDial))
	_, err := d(context.Background(), "tcp", "multi.test:80")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrPrivateIP)
	require.Equal(t, []string{"8.8.8.8:80", "1.1.1.1:80"}, dialed, "every validated IP tried in order")
}

func TestSSRFSafeDialer_DisallowedPort(t *testing.T) {
	for _, port := range []string{"22", "6379", "8080", "8443", "0", ""} {
		t.Run("port "+port, func(t *testing.T) {
			lookupCalled := false
			lookup := func(_ context.Context, _ string) ([]net.IPAddr, error) {
				lookupCalled = true
				return nil, errors.New("must not resolve")
			}
			d := ssrfSafeDialer(lookup, refuseDial)
			_, err := d(context.Background(), "tcp", net.JoinHostPort("example.test", port))
			require.ErrorIs(t, err, ErrDisallowedPort)
			require.False(t, lookupCalled, "port policy must fail before any DNS lookup")
		})
	}
	// Allowed ports proceed past the port check to resolution.
	for _, port := range []string{"80", "443"} {
		t.Run("port "+port+" allowed", func(t *testing.T) {
			sentinel := errors.New("reached lookup")
			lookup := func(_ context.Context, _ string) ([]net.IPAddr, error) { return nil, sentinel }
			d := ssrfSafeDialer(lookup, refuseDial)
			_, err := d(context.Background(), "tcp", net.JoinHostPort("example.test", port))
			require.ErrorIs(t, err, sentinel)
		})
	}
}

func TestFetchURL_DisallowedPort(t *testing.T) {
	// End-to-end through the real fetcher: the port check fires inside the
	// dialer before DNS resolution, so no network I/O happens.
	_, err := NewURLFetcher().FetchURL(context.Background(), "http://192.0.2.1:8080/")
	require.ErrorIs(t, err, ErrDisallowedPort)
}

func TestCheckRedirect_Policy(t *testing.T) {
	req := func(rawURL string) *http.Request {
		u, err := url.Parse(rawURL)
		require.NoError(t, err)
		return &http.Request{URL: u}
	}
	tests := []struct {
		name    string
		target  string
		via     []string
		wantErr error
	}{
		{"https to http downgrade", "http://b.test/", []string{"https://a.test/"}, ErrUnsafeScheme},
		{"downgrade after upgrade", "http://c.test/", []string{"http://a.test/", "https://b.test/"}, ErrUnsafeScheme},
		{"http to https upgrade", "https://b.test/", []string{"http://a.test/"}, nil},
		{"http to http", "http://b.test/", []string{"http://a.test/"}, nil},
		{"https to https", "https://b.test/", []string{"https://a.test/"}, nil},
		{"non-http scheme", "ftp://b.test/", []string{"http://a.test/"}, ErrUnsafeScheme},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			via := make([]*http.Request, len(tt.via))
			for i, v := range tt.via {
				via[i] = req(v)
			}
			err := checkRedirect(req(tt.target), via)
			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
	t.Run("too many redirects", func(t *testing.T) {
		via := make([]*http.Request, 10)
		for i := range via {
			via[i] = req("https://a.test/")
		}
		require.Error(t, checkRedirect(req("https://b.test/"), via))
	})
}

func TestFetchURL_RedirectDowngradeBlocked(t *testing.T) {
	// A TLS server redirecting to plain http must be rejected by CheckRedirect
	// before the downgrade target is ever contacted (.invalid never resolves,
	// and the error must surface before any such attempt anyway).
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://downgrade.invalid/", http.StatusFound)
	}))
	defer srv.Close()
	f := &URLFetcher{client: &http.Client{
		Transport:     srv.Client().Transport,
		CheckRedirect: checkRedirect,
	}}
	_, err := f.FetchURL(context.Background(), srv.URL)
	require.ErrorIs(t, err, ErrUnsafeScheme)
}

// routedFetcher builds a URLFetcher whose dialer runs the full SSRF policy
// (fake resolver, so no real DNS) but physically routes every permitted dial to
// the local test server, so nothing leaves the machine.
func routedFetcher(t *testing.T, srv *httptest.Server, hosts map[string]string, dialed *[]string, mu *sync.Mutex) *URLFetcher {
	t.Helper()
	lookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		ip, ok := hosts[host]
		if !ok {
			return nil, fmt.Errorf("unknown test host %s", host)
		}
		return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
	}
	dial := recordingDialer(dialed, mu, func(_ context.Context, _, _ string) (net.Conn, error) {
		return net.Dial("tcp", srv.Listener.Addr().String())
	})
	transport := &http.Transport{DialContext: ssrfSafeDialer(lookup, dial)}
	t.Cleanup(transport.CloseIdleConnections)
	return &URLFetcher{client: &http.Client{Transport: transport, CheckRedirect: checkRedirect}}
}

func TestFetchURL_RedirectHopRevalidatedByDialer(t *testing.T) {
	// A first hop to a public-resolving host succeeds; its redirect target
	// resolves to a private IP and must be rejected by the dialer on the second
	// hop -- proving every redirect hop re-runs the SSRF checks.
	var dialed []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://internal.test/secrets", http.StatusFound)
	}))
	defer srv.Close()
	f := routedFetcher(t, srv, map[string]string{
		"pub.test":      "8.8.8.8",
		"internal.test": "10.0.0.5",
	}, &dialed, &mu)
	_, err := f.FetchURL(context.Background(), "http://pub.test/")
	require.ErrorIs(t, err, ErrPrivateIP)
	require.Equal(t, []string{"8.8.8.8:80"}, dialed, "the private redirect target must never be dialed")
}

func TestFetchURL_SucceedsThroughPinnedDialer(t *testing.T) {
	// Positive control for the refactored dialer: a public-resolving host is
	// fetched through the full SSRF path with the dial pinned to the validated IP.
	var dialed []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Pinned</title></head><body><p>ok</p></body></html>`))
	}))
	defer srv.Close()
	f := routedFetcher(t, srv, map[string]string{"pub.test": "8.8.8.8"}, &dialed, &mu)
	c, err := f.FetchURL(context.Background(), "http://pub.test/")
	require.NoError(t, err)
	require.Equal(t, "Pinned", c.Title)
	require.Equal(t, []string{"8.8.8.8:80"}, dialed)
}

func TestIsPrivateIP(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1": true, "10.1.2.3": true, "192.168.1.1": true,
		"169.254.1.1": true, "0.0.0.0": true, "::1": true,
		"8.8.8.8": false, "1.1.1.1": false,
		// Reserved / internal-use ranges beyond the stdlib helpers.
		"0.0.0.5":         true, // "this host" block routes to localhost on Linux
		"100.100.1.1":     true, // CGNAT / shared address space (tailnets)
		"192.0.0.10":      true, // IETF protocol assignments
		"198.18.0.5":      true, // benchmarking
		"240.0.0.1":       true, // reserved
		"255.255.255.255": true, // broadcast
		"224.0.0.251":     true, // multicast (mDNS)
		"fd00::1":         true, // IPv6 ULA
		"ff02::1":         true, // IPv6 multicast
		"::ffff:10.0.0.1": true, // IPv4-mapped private
		// NAT64: privateness follows the embedded IPv4 address.
		"64:ff9b::7f00:1":  true,  // embeds 127.0.0.1
		"64:ff9b::808:808": false, // embeds 8.8.8.8
		"2607:f8b0::1":     false, // public IPv6
	}
	for s, want := range cases {
		require.Equal(t, want, isPrivateIP(net.ParseIP(s)), s)
	}
	require.True(t, isPrivateIP(nil), "unparseable addresses fail closed")
}
