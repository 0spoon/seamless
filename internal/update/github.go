package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// ReleasesURL lists the newest releases. The list endpoint, not
	// /releases/latest: "latest" is the most recently created release, and a
	// backport makes that an older version (Newest). per_page=20 covers months
	// of releases; at 13 assets a release the response is about 0.5 MB.
	ReleasesURL = "https://api.github.com/repos/" + Repo + "/releases?per_page=20"

	// UserAgent is the fixed User-Agent of every update request. It carries no
	// version, host or install identifier: the check is anonymous.
	UserAgent = "seamlessd-update-check"

	// maxListBytes caps the release-list body. The list is about 0.5 MB today,
	// so 4 MiB leaves room for growth while still bounding a misrouted reply.
	maxListBytes = 4 << 20

	// fetchTimeout bounds one whole request, body included.
	fetchTimeout = 30 * time.Second

	// minRetryWait is the shortest wait a rate-limit answer ever produces, and
	// maxRetryWait the longest: a Reset or Retry-After outside that range is
	// either imminent or garbage, and neither should hammer or silence the
	// check.
	minRetryWait = 15 * time.Minute
	maxRetryWait = 24 * time.Hour
)

// ErrUnavailable means the release list could not be read this time: a
// transport failure, a non-200 status that is not rate limiting, an oversized
// or undecodable body. It is remote and may clear on its own, so the checker
// backs off and tries again.
var ErrUnavailable = errors.New("update: release list unavailable")

// ErrRateLimited matches a *RateLimitError under errors.Is.
var ErrRateLimited = errors.New("update: GitHub API rate limited")

// RateLimitError is a 403 or 429 that GitHub marked as rate limiting. RetryAt
// is when the next request may be made, on the local clock.
type RateLimitError struct {
	RetryAt time.Time
	Status  string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("update: GitHub API rate limited (%s); next request after %s",
		e.Status, e.RetryAt.UTC().Format(time.RFC3339))
}

// Is lets errors.Is(err, ErrRateLimited) match.
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// Page is one answer from the release list.
type Page struct {
	Releases []APIRelease
	// ETag validates the cached list on the next request (If-None-Match).
	ETag string
	// ServerDate is the response's Date header: GitHub's clock, which the soak
	// arithmetic trusts over the local one. Zero when absent or unparseable.
	ServerDate time.Time
	// NotModified is a 304: the list the caller cached under ETag is current,
	// and Releases is empty.
	NotModified bool
}

// Fetcher reads the release list from GitHub.
//
// Its client is deliberately not config.HTTPClient: that constructor is for
// credential-bearing dials to a Seamless daemon (constraint
// seamless-http-client-is-config-httpclient). This is an anonymous request to
// a public host with the system trust store, so it uses Go's default TLS, the
// environment's proxy, and https-only redirects -- and never sends a token,
// even when GITHUB_TOKEN is set.
type Fetcher struct {
	Client *http.Client
	URL    string
	Now    func() time.Time
}

// NewFetcher returns a Fetcher for the real release list.
func NewFetcher() *Fetcher {
	return &Fetcher{Client: NewHTTPClient(), URL: ReleasesURL, Now: time.Now}
}

// NewHTTPClient is the client every update request uses: the default
// transport's TLS and connection settings, proxy from the environment, a
// whole-request timeout, and no redirect that leaves https.
func NewHTTPClient() *http.Client {
	var tr *http.Transport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = dt.Clone()
	} else {
		tr = &http.Transport{}
	}
	tr.Proxy = http.ProxyFromEnvironment
	return &http.Client{Transport: tr, Timeout: fetchTimeout, CheckRedirect: HTTPSOnlyRedirect}
}

// Fetch asks for the release list, conditionally on etag when it is set. A
// 304 comes back as a Page with NotModified and the same ETag. Errors are
// either a *RateLimitError (errors.Is ErrRateLimited) or wrap ErrUnavailable,
// except a ctx cancellation, which wraps the ctx's error as well.
func (f *Fetcher) Fetch(ctx context.Context, etag string) (Page, error) {
	if err := RequireHTTPS(f.URL); err != nil {
		return Page{}, fmt.Errorf("update.Fetch: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return Page{}, fmt.Errorf("update.Fetch: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", UserAgent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := f.Client.Do(req)
	if err != nil {
		return Page{}, fmt.Errorf("update.Fetch: %w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	page := Page{ETag: resp.Header.Get("ETag"), ServerDate: parseHTTPDate(resp.Header.Get("Date"))}
	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes+1))
		if err != nil {
			return Page{}, fmt.Errorf("update.Fetch: read release list: %w: %w", ErrUnavailable, err)
		}
		if len(body) > maxListBytes {
			return Page{}, fmt.Errorf("update.Fetch: %w: release list exceeds %d bytes", ErrUnavailable, maxListBytes)
		}
		if err := json.Unmarshal(body, &page.Releases); err != nil {
			return Page{}, fmt.Errorf("update.Fetch: %w: decode release list: %w", ErrUnavailable, err)
		}
		return page, nil
	case http.StatusNotModified:
		page.NotModified = true
		if page.ETag == "" {
			page.ETag = etag
		}
		return page, nil
	case http.StatusForbidden, http.StatusTooManyRequests:
		return Page{}, f.limitError(resp, page.ServerDate)
	default:
		return Page{}, fmt.Errorf("update.Fetch: %w: unexpected status %s", ErrUnavailable, resp.Status)
	}
}

// limitError classifies a 403 or 429. GitHub marks its primary rate limit with
// X-RateLimit-Remaining: 0 plus X-RateLimit-Reset (epoch seconds), and its
// secondary limits with Retry-After; a 429 is rate limiting by definition. A
// 403 carrying none of those is an ordinary refusal, not a reason to wait for
// a reset, so it is ErrUnavailable.
//
// Reset is converted to a wait against GitHub's own Date header when there is
// one, so a local clock that is off by days neither silences the check for
// days nor retries immediately. The wait is clamped to [15m, 24h].
func (f *Fetcher) limitError(resp *http.Response, serverDate time.Time) error {
	now := time.Now()
	if f.Now != nil {
		now = f.Now()
	}
	h := resp.Header
	limited := resp.StatusCode == http.StatusTooManyRequests
	var wait time.Duration
	if strings.TrimSpace(h.Get("X-RateLimit-Remaining")) == "0" {
		limited = true
		if reset, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64); err == nil {
			ref := now
			if !serverDate.IsZero() {
				ref = serverDate
			}
			wait = max(wait, time.Unix(reset, 0).Sub(ref))
		}
	}
	if ra := strings.TrimSpace(h.Get("Retry-After")); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
			limited = true
			wait = max(wait, time.Duration(secs)*time.Second)
		} else if at, err := http.ParseTime(ra); err == nil {
			limited = true
			ref := now
			if !serverDate.IsZero() {
				ref = serverDate
			}
			wait = max(wait, at.Sub(ref))
		}
	}
	if !limited {
		return fmt.Errorf("update.Fetch: %w: %s", ErrUnavailable, resp.Status)
	}
	wait = min(max(wait, minRetryWait), maxRetryWait)
	return &RateLimitError{RetryAt: now.Add(wait).UTC(), Status: resp.Status}
}

// parseHTTPDate parses an HTTP Date header, returning zero when it is absent
// or malformed.
func parseHTTPDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := http.ParseTime(s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
