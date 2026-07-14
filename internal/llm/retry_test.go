package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fastRetry keeps the retry tests quick while preserving the attempt bound.
var fastRetry = retryPolicy{maxAttempts: 3, baseDelay: time.Millisecond, maxDelay: 5 * time.Millisecond}

// flakyServer fails the first failures requests with failStatus, then serves
// okBody with 200. The returned counter records total requests received.
func flakyServer(t *testing.T, failures int, failStatus int, okBody string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if int(calls.Add(1)) <= failures {
			w.WriteHeader(failStatus)
			_, _ = w.Write([]byte(`{"error":"transient"}`))
			return
		}
		_, _ = w.Write([]byte(okBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// Transient statuses (429, 5xx) are retried to eventual success on every
// provider's chat client, including Anthropic's 529 overloaded.
func TestChatRetriesTransientThenSucceeds(t *testing.T) {
	cases := []struct {
		name   string
		status int
		okBody string
		mk     func(u string) Chat
	}{
		{"openai 429", http.StatusTooManyRequests,
			`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`,
			func(u string) Chat {
				c := newOpenAIChat("k", u, "m")
				c.client.policy = fastRetry
				return c
			}},
		{"ollama 500", http.StatusInternalServerError,
			`{"message":{"role":"assistant","content":"ok"}}`,
			func(u string) Chat {
				c := newOllamaChat(u, "m")
				c.client.policy = fastRetry
				return c
			}},
		{"anthropic 529", 529,
			`{"content":[{"type":"text","text":"ok"}]}`,
			func(u string) Chat {
				c := newAnthropicChat("k", u, "m")
				c.client.policy = fastRetry
				return c
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := flakyServer(t, 2, tc.status, tc.okBody)
			out, err := tc.mk(srv.URL).Complete(context.Background(), "sys", "user")
			require.NoError(t, err)
			require.Equal(t, "ok", out)
			require.Equal(t, int32(3), calls.Load(), "two failures then success")
		})
	}
}

// Embedders share the same retry behavior as chat.
func TestEmbedRetriesTransientThenSucceeds(t *testing.T) {
	t.Run("openai 503", func(t *testing.T) {
		srv, calls := flakyServer(t, 1, http.StatusServiceUnavailable, `{"data":[{"embedding":[0.5]}]}`)
		e := NewOpenAIEmbedder("k", srv.URL, "m", 0)
		e.client.policy = fastRetry
		vec, err := e.Embed(context.Background(), "x")
		require.NoError(t, err)
		require.Equal(t, []float32{0.5}, vec)
		require.Equal(t, int32(2), calls.Load())
	})
	t.Run("ollama 429", func(t *testing.T) {
		srv, calls := flakyServer(t, 1, http.StatusTooManyRequests, `{"embeddings":[[0.1]]}`)
		e := NewOllamaEmbedder(srv.URL, "m")
		e.client.policy = fastRetry
		vec, err := e.Embed(context.Background(), "x")
		require.NoError(t, err)
		require.Equal(t, []float32{0.1}, vec)
		require.Equal(t, int32(2), calls.Load())
	})
}

// A transport-level failure (connection killed mid-request) is retried too.
func TestRetryTransportErrorThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			hj, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, _, err := hj.Hijack()
			require.NoError(t, err)
			_ = conn.Close() // client sees an abrupt EOF
			return
		}
		_, _ = w.Write([]byte(`{"embeddings":[[0.2]]}`))
	}))
	t.Cleanup(srv.Close)

	e := NewOllamaEmbedder(srv.URL, "m")
	e.client.policy = fastRetry
	vec, err := e.Embed(context.Background(), "x")
	require.NoError(t, err)
	require.Equal(t, []float32{0.2}, vec)
	require.Equal(t, int32(2), calls.Load())
}

// Non-retryable statuses fail on the first attempt with the mapped sentinel.
func TestRetryNonRetryableFailsImmediately(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"400 bad request", http.StatusBadRequest, ErrUnavailable},
		{"401 unauthorized", http.StatusUnauthorized, ErrAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := flakyServer(t, 1000, tc.status, "")
			c := newOpenAIChat("k", srv.URL, "m")
			c.client.policy = fastRetry
			_, err := c.Complete(context.Background(), "", "x")
			require.Error(t, err)
			require.ErrorIs(t, err, tc.want)
			require.Equal(t, int32(1), calls.Load(), "must not retry a %d", tc.status)
		})
	}
}

// A persistent 429 is retried exactly maxAttempts times, then surfaces
// ErrRateLimited from the final response.
func TestRetryExhaustsAttempts(t *testing.T) {
	srv, calls := flakyServer(t, 1000, http.StatusTooManyRequests, "")
	e := NewOpenAIEmbedder("k", srv.URL, "m", 0)
	e.client.policy = fastRetry
	_, err := e.Embed(context.Background(), "x")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrRateLimited)
	require.Equal(t, int32(3), calls.Load())
}

// A Retry-After hint stretches the backoff: with a millisecond base delay the
// only way the second attempt lands a second later is the header being honored.
func TestRetryHonorsRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)

	c := newOpenAIChat("k", srv.URL, "m")
	c.client.policy = retryPolicy{maxAttempts: 2, baseDelay: time.Millisecond, maxDelay: 2 * time.Second}
	start := time.Now()
	out, err := c.Complete(context.Background(), "", "x")
	require.NoError(t, err)
	require.Equal(t, "ok", out)
	require.Equal(t, int32(2), calls.Load())
	require.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond)
}

func TestRetryAfterHint(t *testing.T) {
	mk := func(v string) *http.Response {
		resp := &http.Response{Header: http.Header{}}
		if v != "" {
			resp.Header.Set("Retry-After", v)
		}
		return resp
	}

	require.Equal(t, time.Duration(0), retryAfterHint(mk("")))
	require.Equal(t, 3*time.Second, retryAfterHint(mk("3")))
	require.Equal(t, time.Duration(0), retryAfterHint(mk("0")))
	require.Equal(t, time.Duration(0), retryAfterHint(mk("-5")))
	require.Equal(t, time.Duration(0), retryAfterHint(mk("soon")))

	future := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	d := retryAfterHint(mk(future))
	require.Greater(t, d, time.Duration(0))
	require.LessOrEqual(t, d, 2*time.Second)

	past := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
	require.Equal(t, time.Duration(0), retryAfterHint(mk(past)))
}

// Cancelling the context aborts the backoff wait immediately; no further
// attempt is made and the cancellation is visible in the error chain.
func TestRetryContextCancellationStopsRetries(t *testing.T) {
	srv, calls := flakyServer(t, 1000, http.StatusInternalServerError, "")
	c := newOpenAIChat("k", srv.URL, "m")
	c.client.policy = retryPolicy{maxAttempts: 3, baseDelay: 10 * time.Second, maxDelay: 10 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := c.Complete(ctx, "", "x")
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 5*time.Second, "must not sit out the 10s backoff")
	require.Equal(t, int32(1), calls.Load(), "no attempt after cancellation")
}
