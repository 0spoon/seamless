package llm

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Every Seamless LLM call is an idempotent POST to an inference API, so a
// transient failure (429 throttling, 5xx, transport error) is always safe to
// replay. retryClient centralizes that policy for all providers, chat and
// embeddings alike: a small bounded number of attempts with exponential
// backoff plus jitter, honoring a server Retry-After hint, and aborting
// promptly on context cancellation.

// retryPolicy bounds the shared retry behavior.
type retryPolicy struct {
	// maxAttempts is the total number of tries, including the first.
	maxAttempts int
	// baseDelay is the pre-jitter backoff before the first retry; it doubles
	// for each subsequent retry.
	baseDelay time.Duration
	// maxDelay caps both the computed backoff and a server Retry-After hint,
	// so a single hostile or confused header cannot stall a pass.
	maxDelay time.Duration
}

// defaultRetryPolicy keeps retries cheap: at most two replays, sub-10s waits.
// The per-call timeouts (chatTimeout, embedTimeout) still bound the total.
var defaultRetryPolicy = retryPolicy{
	maxAttempts: 3,
	baseDelay:   500 * time.Millisecond,
	maxDelay:    8 * time.Second,
}

// retryClient wraps an http.Client with the shared retry policy. The zero
// value is not usable; build one with newRetryClient.
type retryClient struct {
	hc     *http.Client
	policy retryPolicy
}

func newRetryClient(timeout time.Duration) retryClient {
	return retryClient{hc: &http.Client{Timeout: timeout}, policy: defaultRetryPolicy}
}

// do sends the request built by newReq, retrying transport errors and
// retryable statuses (429 and any 5xx). A fresh request is built for every
// attempt so a consumed body reader is never replayed. The final response --
// success or terminal failure -- is returned unconsumed for the caller's
// status mapping; only intermediate retryable responses are drained and
// closed here. Context cancellation aborts promptly: nothing sleeps past
// ctx.Done(), and a cancelled context is never retried.
func (rc retryClient) do(ctx context.Context, newReq func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		req, err := newReq()
		if err != nil {
			return nil, fmt.Errorf("new request: %w", err)
		}
		resp, err := rc.hc.Do(req)
		if err != nil {
			if attempt >= rc.policy.maxAttempts || ctx.Err() != nil {
				return nil, err
			}
			if serr := rc.sleep(ctx, attempt, 0); serr != nil {
				return nil, fmt.Errorf("%w; retry canceled: %w", err, serr)
			}
			continue
		}
		if attempt >= rc.policy.maxAttempts || !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
		status := resp.StatusCode
		hint := retryAfterHint(resp)
		// Drain a bounded amount so the keep-alive connection stays reusable.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if serr := rc.sleep(ctx, attempt, hint); serr != nil {
			return nil, fmt.Errorf("status %d; retry canceled: %w", status, serr)
		}
	}
}

// retryableStatus reports whether a response status is worth replaying:
// throttling (429) and server-side failures (5xx, which covers Anthropic's
// 529 overloaded). Every other status -- notably 4xx validation and auth
// errors -- fails immediately.
func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// sleep blocks for the backoff preceding the retry after completed attempt
// number attempt (1-based), stretching to a server Retry-After hint when the
// hint exceeds the computed backoff. It returns ctx.Err() the moment the
// context ends, without finishing the wait.
func (rc retryClient) sleep(ctx context.Context, attempt int, hint time.Duration) error {
	d := rc.policy.baseDelay << (attempt - 1)
	if d <= 0 || d > rc.policy.maxDelay {
		d = rc.policy.maxDelay
	}
	// Equal jitter: keep half the backoff as a floor, randomize the rest so
	// concurrent callers do not retry in lockstep.
	d = d/2 + rand.N(d/2+1)
	if hint > d {
		d = min(hint, rc.policy.maxDelay)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryAfterHint parses a Retry-After header as either delta-seconds or an
// HTTP-date. It returns 0 when the header is absent, unparseable, or already
// in the past.
func retryAfterHint(resp *http.Response) time.Duration {
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}
