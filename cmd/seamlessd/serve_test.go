package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Regression: with a long-lived stream open (the console's SSE feed blocks on
// r.Context().Done() until the client goes away), Shutdown must still complete
// promptly once the serve ctx is cancelled. Without the server's BaseContext
// wiring the request context never cancels, the handler never returns, and
// every daemon shutdown stalls for the full Shutdown deadline and then fails.
func TestNewHTTPServer_ShutdownUnblocksStreamingHandlers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entered := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			fmt.Fprint(w, ": connected\n\n")
			f.Flush()
		}
		close(entered)
		<-r.Context().Done() // the SSE loop's lifetime
	})

	srv := newHTTPServer(ctx, "127.0.0.1:0", mux)
	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()

	// Open the stream and wait until the handler is parked in its event loop.
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, "http://"+ln.Addr().String()+"/stream", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("streaming handler never started")
	}

	// Shutdown as the daemon does: cancel the serve ctx, then Shutdown with a
	// deadline. The stream must unblock well before the deadline.
	cancel()
	shutdownCtx, sdCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer sdCancel()
	start := time.Now()
	require.NoError(t, srv.Shutdown(shutdownCtx),
		"shutdown must not stall on an open stream")
	require.Less(t, time.Since(start), 2*time.Second)
}
