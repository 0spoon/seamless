// Package capture fetches external URLs into note content behind SSRF guards. It
// is ported and trimmed from Seam v1 internal/capture: the URL path is kept
// (private-IP rejection, DNS-rebinding-safe pinned dialer, port allowlist,
// redirect-scheme and downgrade validation, response size cap); voice
// transcription is dropped.
package capture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// Domain errors.
var (
	ErrInvalidURL     = errors.New("invalid URL")
	ErrFetchFailed    = errors.New("URL fetch failed")
	ErrPrivateIP      = errors.New("URL points to private/loopback address")
	ErrUnsafeScheme   = errors.New("URL scheme not allowed")
	ErrDisallowedPort = errors.New("URL port not allowed")
)

// userAgent identifies Seamless when fetching a page.
const userAgent = "Seamless (agent-memory)"

// URLFetcher fetches and extracts readable content from URLs behind SSRF guards.
type URLFetcher struct {
	client *http.Client
}

// allowedRedirectSchemes are the only URL schemes allowed in redirect targets.
var allowedRedirectSchemes = map[string]bool{"http": true, "https": true}

// defaultAllowedPorts are the destination ports capture dials when the caller
// supplies no allowlist. The same list is config's capture.allowed_ports default;
// it is spelled out in both places because capture may import config but not the
// reverse, and a two-element literal is not worth an import for.
var defaultAllowedPorts = []int{80, 443}

// portSet turns an allowlist into the set the dialer checks. An empty or nil
// list is NOT "every port allowed": it falls back to defaultAllowedPorts, so an
// omitted or blanked-out config can never silently switch the port guard off.
// Range sanity (1-65535) is config.Validate's job, which rejects a bad port at
// load rather than letting it reach here.
func portSet(ports []int) map[int]bool {
	if len(ports) == 0 {
		ports = defaultAllowedPorts
	}
	set := make(map[int]bool, len(ports))
	for _, p := range ports {
		set[p] = true
	}
	return set
}

// NewURLFetcher creates a URLFetcher whose transport rejects private/loopback
// addresses and ports outside allowedPorts (connecting only to a validated IP,
// so DNS rebinding cannot slip past the check) and caps redirects at 10, each hop
// re-validated for an HTTP(S) scheme, no https->http downgrade, and -- because
// every hop dials through the same transport -- a public IP on an allowed port.
// An empty allowedPorts means the 80/443 default, never "any port".
func NewURLFetcher(allowedPorts []int) *URLFetcher {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: ssrfSafeDialer(net.DefaultResolver.LookupIPAddr, dialer.DialContext, portSet(allowedPorts)),
	}
	return &URLFetcher{
		client: &http.Client{
			Timeout:       15 * time.Second,
			Transport:     transport,
			CheckRedirect: checkRedirect,
		},
	}
}

// checkRedirect enforces the redirect policy: at most 10 hops, HTTP(S) targets
// only, and never a downgrade to http once any hop in the chain used https.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("too many redirects")
	}
	scheme := strings.ToLower(req.URL.Scheme)
	if !allowedRedirectSchemes[scheme] {
		return fmt.Errorf("%w: redirect to %s", ErrUnsafeScheme, scheme)
	}
	if scheme == "http" {
		for _, prev := range via {
			if strings.EqualFold(prev.URL.Scheme, "https") {
				return fmt.Errorf("%w: redirect downgrades https to http", ErrUnsafeScheme)
			}
		}
	}
	return nil
}

// lookupIPFunc resolves a hostname to its addresses; injectable for tests.
type lookupIPFunc func(ctx context.Context, host string) ([]net.IPAddr, error)

// dialContextFunc matches net.Dialer.DialContext; injectable for tests.
type dialContextFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// ssrfSafeDialer returns a DialContext that enforces the SSRF policy at the
// single point every connection (initial request and each redirect hop) passes
// through: the destination port must be in allowedPorts, and the host must
// resolve to exclusively non-private addresses -- one private record among
// public ones rejects the whole lookup (fail closed; public hostnames have no
// business mixing in private records). It then dials the validated IP literals
// themselves, in resolution order until one connects, so the connection is
// pinned to an address that passed validation and a racing resolver (DNS
// rebinding) cannot swap in a different one between check and dial.
//
// allowedPorts is passed in rather than read from a package var so the policy is
// per-fetcher and configurable; keep the check here, since a port check anywhere
// above the dialer (say in FetchURL) is bypassable by a redirect hop.
func ssrfSafeDialer(lookup lookupIPFunc, dial dialContextFunc, allowedPorts map[int]bool) dialContextFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("capture.ssrfSafeDialer: %w", err)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || !allowedPorts[port] {
			return nil, fmt.Errorf("capture.ssrfSafeDialer: %w: %q", ErrDisallowedPort, portStr)
		}
		ips, err := lookup(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("capture.ssrfSafeDialer: resolve: %w", err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("capture.ssrfSafeDialer: no addresses for %s", host)
		}
		for _, ip := range ips {
			if isPrivateIP(ip.IP) {
				return nil, fmt.Errorf("capture.ssrfSafeDialer: %s: %w", host, ErrPrivateIP)
			}
		}
		var errs error
		for _, ip := range ips {
			conn, dialErr := dial(ctx, network, net.JoinHostPort(ip.IP.String(), portStr))
			if dialErr == nil {
				return conn, nil
			}
			errs = errors.Join(errs, dialErr)
		}
		return nil, fmt.Errorf("capture.ssrfSafeDialer: dial %s: %w", host, errs)
	}
}

// reservedNets are non-global ranges the stdlib net.IP helpers do not cover:
// "this host" (0.0.0.0/8 routes to localhost on Linux), CGNAT / shared address
// space (100.64.0.0/10, also used by tailnets), IETF protocol assignments
// (192.0.0.0/24), benchmarking (198.18.0.0/15), and reserved-plus-broadcast
// (240.0.0.0/4).
var reservedNets = mustCIDRs(
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4",
)

// nat64Prefix is the RFC 6052 well-known NAT64 prefix; an address inside it
// stands for the IPv4 address embedded in its last four bytes, which decides
// privateness (so a NAT64 mapping cannot smuggle in a private target).
var nat64Prefix = mustCIDRs("64:ff9b::/96")[0]

// mustCIDRs parses literal CIDRs at package init; a bad literal is a programmer
// error (caught by any test run), analogous to regexp.MustCompile.
func mustCIDRs(cidrs ...string) []*net.IPNet {
	nets := make([]*net.IPNet, len(cidrs))
	for i, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("capture: bad reserved CIDR " + c)
		}
		nets[i] = n
	}
	return nets
}

// isPrivateIP reports whether ip must not be fetched: loopback, private,
// link-local, multicast, unspecified, a reserved/internal-use range, or a NAT64
// mapping of any of those. A nil ip (unparseable) is rejected too (fail closed).
func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, n := range reservedNets {
		if n.Contains(ip) {
			return true
		}
	}
	if nat64Prefix.Contains(ip) {
		return isPrivateIP(net.IP(ip.To16()[12:16]))
	}
	return false
}

// URLContent holds the extracted content from a fetched URL.
type URLContent struct {
	Title string
	Body  string
	URL   string
}

// FetchURL fetches rawURL and extracts its title and main readable content. It
// rejects non-HTTP(S) schemes and empty hosts before dialing; the transport's
// SSRF guard rejects private addresses at connect time.
func (f *URLFetcher) FetchURL(ctx context.Context, rawURL string) (*URLContent, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("capture.FetchURL: %w: %w", ErrInvalidURL, err)
	}
	if scheme := strings.ToLower(parsed.Scheme); scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("capture.FetchURL: %w: %s", ErrUnsafeScheme, scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("capture.FetchURL: %w: empty host", ErrInvalidURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("capture.FetchURL: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("capture.FetchURL: %w: %w", ErrFetchFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("capture.FetchURL: %w: status %d", ErrFetchFailed, resp.StatusCode)
	}

	// Cap the read at 2MB and normalize charset to UTF-8.
	limited := io.LimitReader(resp.Body, 2*1024*1024)
	utf8Reader, err := charset.NewReader(limited, resp.Header.Get("Content-Type"))
	if err != nil {
		utf8Reader = limited // fall back to raw bytes if charset detection fails
	}
	doc, err := html.Parse(utf8Reader)
	if err != nil {
		return nil, fmt.Errorf("capture.FetchURL: parse html: %w", err)
	}

	title := extractTitle(doc)
	if title == "" {
		title = extractOGTitle(doc)
	}
	if title == "" {
		title = parsed.Host + parsed.Path // last resort
	}
	return &URLContent{Title: title, Body: extractMainContent(doc), URL: rawURL}, nil
}

// extractOGTitle looks for <meta property="og:title" content="...">.
func extractOGTitle(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "meta" {
		var property, content string
		for _, attr := range n.Attr {
			switch attr.Key {
			case "property":
				property = attr.Val
			case "content":
				content = attr.Val
			}
		}
		if property == "og:title" && content != "" {
			return strings.TrimSpace(content)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if t := extractOGTitle(c); t != "" {
			return t
		}
	}
	return ""
}

// extractTitle finds the <title> element text.
func extractTitle(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "title" {
		return strings.TrimSpace(collectText(n))
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if t := extractTitle(c); t != "" {
			return t
		}
	}
	return ""
}

// extractMainContent extracts the main readable content: <article>, then <main>,
// then <body>.
func extractMainContent(doc *html.Node) string {
	for _, tag := range []string{"article", "main", "body"} {
		if node := findElement(doc, tag); node != nil {
			if text := cleanText(collectText(node)); text != "" {
				return text
			}
		}
	}
	return ""
}

// findElement finds the first element with the given tag name, depth-first.
func findElement(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}

// collectText recursively collects text from a node, skipping script/style/nav.
func collectText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	if n.Type == html.ElementNode {
		switch n.Data {
		case "script", "style", "noscript", "nav", "footer", "header":
			return ""
		case "br":
			return "\n"
		case "p", "div", "section", "article", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6":
			var sb strings.Builder
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				sb.WriteString(collectText(c))
			}
			return sb.String() + "\n\n"
		}
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(collectText(c))
	}
	return sb.String()
}

// cleanText normalizes whitespace: collapses blank-line runs and trims.
func cleanText(s string) string {
	var cleaned []string
	prevBlank := false
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if !prevBlank {
				cleaned = append(cleaned, "")
				prevBlank = true
			}
			continue
		}
		prevBlank = false
		cleaned = append(cleaned, line)
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}
