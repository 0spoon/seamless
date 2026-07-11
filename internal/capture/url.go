// Package capture fetches external URLs into note content behind SSRF guards. It
// is ported and trimmed from Seam v1 internal/capture: the URL path is kept
// as-is (private-IP rejection, DNS-rebinding-safe dialer, redirect-scheme
// validation, response size cap); voice transcription is dropped.
package capture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// Domain errors.
var (
	ErrInvalidURL   = errors.New("invalid URL")
	ErrFetchFailed  = errors.New("URL fetch failed")
	ErrPrivateIP    = errors.New("URL points to private/loopback address")
	ErrUnsafeScheme = errors.New("URL scheme not allowed")
)

// userAgent identifies Seamless when fetching a page.
const userAgent = "Seamless (agent-memory)"

// URLFetcher fetches and extracts readable content from URLs behind SSRF guards.
type URLFetcher struct {
	client *http.Client
}

// allowedRedirectSchemes are the only URL schemes allowed in redirect targets.
var allowedRedirectSchemes = map[string]bool{"http": true, "https": true}

// NewURLFetcher creates a URLFetcher whose transport rejects private/loopback
// addresses (connecting to the validated IP, so DNS rebinding cannot slip past
// the check) and caps redirects at 10, each re-validated for an HTTP(S) scheme.
func NewURLFetcher() *URLFetcher {
	transport := &http.Transport{DialContext: ssrfSafeDialer}
	return &URLFetcher{
		client: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				if scheme := strings.ToLower(req.URL.Scheme); !allowedRedirectSchemes[scheme] {
					return fmt.Errorf("%w: redirect to %s", ErrUnsafeScheme, scheme)
				}
				return nil
			},
		},
	}
}

// ssrfSafeDialer rejects connections to private/loopback addresses and connects
// to the validated IP directly to prevent DNS rebinding (TOCTOU).
func ssrfSafeDialer(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("capture.ssrfSafeDialer: %w", err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("capture.ssrfSafeDialer: resolve: %w", err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("capture.ssrfSafeDialer: no addresses for %s", host)
	}
	for _, ip := range ips {
		if isPrivateIP(ip.IP) {
			return nil, ErrPrivateIP
		}
	}
	validatedAddr := net.JoinHostPort(ips[0].IP.String(), port)
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, validatedAddr)
}

// isPrivateIP reports whether ip is loopback, private, link-local, or
// unspecified (0.0.0.0 routes to localhost on many OSes).
func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
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
