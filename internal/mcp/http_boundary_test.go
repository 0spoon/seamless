package mcp_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	seammcp "github.com/0spoon/seamless/internal/mcp"
)

type countingBody struct {
	r     io.Reader
	reads int
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.reads += n
	return n, err
}

func (*countingBody) Close() error { return nil }

func TestHTTPBoundary_AuthenticatesEveryProtocolMethod(t *testing.T) {
	url, db := newServer(t)
	tests := []struct {
		name   string
		method string
		body   string
	}{
		{"get sse", http.MethodGet, ""},
		{"initialize", http.MethodPost, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`},
		{"tools list", http.MethodPost, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`},
		{"delete session", http.MethodDelete, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, header, body := rawMCPRequest(t, url, tt.method, "wrong-key", tt.body)
			require.Equal(t, http.StatusUnauthorized, status)
			require.Equal(t, "Bearer", header.Get("WWW-Authenticate"))
			require.Contains(t, body, "unauthorized")
			require.NotContains(t, body, "project_list", "auth failure must reveal no tool schema")
		})
	}
	require.Empty(t, toolCallEvents(t, db), "transport-level rejections dispatch no tools")
}

func TestHTTPBoundary_RejectsBeforeReadingAndBoundsAuthorizedBodies(t *testing.T) {
	h := seammcp.New(seammcp.Config{APIKey: testKey}).Handler()
	large := strings.Repeat("x", (1<<20)+4096)

	t.Run("unauthorized body is untouched", func(t *testing.T) {
		body := &countingBody{r: strings.NewReader(large)}
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/mcp", nil)
		req.Body = body
		req.ContentLength = int64(len(large))
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code)
		require.Zero(t, body.reads)
	})

	t.Run("known oversized body is 413 without a read", func(t *testing.T) {
		body := &countingBody{r: strings.NewReader(large)}
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/mcp", nil)
		req.Body = body
		req.ContentLength = int64(len(large))
		req.Header.Set("Authorization", "Bearer "+testKey)
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req)

		require.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
		require.Zero(t, body.reads)
	})

	t.Run("unknown length is read only through the cap", func(t *testing.T) {
		body := &countingBody{r: strings.NewReader(large)}
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/mcp", nil)
		req.Body = body
		req.ContentLength = -1
		req.Header.Set("Authorization", "Bearer "+testKey)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req)

		require.LessOrEqual(t, body.reads, (1<<20)+1)
		require.Contains(t, rr.Body.String(), "request body too large")
	})
}

func TestHTTPBoundary_AllowsAuthenticatedSSEAndDelete(t *testing.T) {
	url, _ := newServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")
	cancel()
	_ = resp.Body.Close()

	status, _, _ := rawMCPRequest(t, url, http.MethodDelete, testKey, "")
	require.Equal(t, http.StatusOK, status)
}
