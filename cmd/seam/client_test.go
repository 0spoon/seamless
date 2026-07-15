package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
)

// errServer answers every request with code and body.
func errServer(t *testing.T, code int, body string) config.Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	cfg := config.Defaults()
	cfg.Addr = strings.TrimPrefix(srv.URL, "http://")
	return cfg
}

// consoleJSON threw the console's JSON error body away and reported
// `console returned 400 Bad Request`, naming neither the parameter nor the valid
// values. B5 made the console answer `?status=bogus` with a message that names
// both -- and without this fix the client would have swallowed the very message
// the server had just taken the trouble to write, turning a loud server into a
// useless client.
func TestConsoleJSON_SurfacesTheHandlersErrorBody(t *testing.T) {
	cfg := errServer(t, http.StatusBadRequest,
		`{"error":"invalid status \"bogus\": valid values are active, completed, expired"}`)
	var v struct{}
	err := consoleJSON(cfg, "/console/sessions?format=json", &v)
	require.EqualError(t, err, `invalid status "bogus": valid values are active, completed, expired`)
}

// The fallback when the handler sent no message: the bare status, as before.
func TestConsoleJSON_FallsBackToTheStatus(t *testing.T) {
	cfg := errServer(t, http.StatusInternalServerError, `not json at all`)
	var v struct{}
	err := consoleJSON(cfg, "/console/x", &v)
	require.Error(t, err)
	require.Contains(t, err.Error(), "console returned 500")
}

// 404 keeps its own phrasing: "not found" is what the callers print.
func TestConsoleJSON_NotFound(t *testing.T) {
	cfg := errServer(t, http.StatusNotFound, `{"error":"no such plan"}`)
	var v struct{}
	err := consoleJSON(cfg, "/console/plans/nope", &v)
	require.EqualError(t, err, "not found")
}

// consolePOST already surfaced the body; it now shares consoleError with
// consoleJSON, so the two cannot drift apart again.
func TestConsolePOST_SurfacesTheHandlersErrorBody(t *testing.T) {
	cfg := errServer(t, http.StatusBadRequest, `{"error":"task is not claimed"}`)
	err := consolePOST(cfg, "/console/tasks/x/release?format=json", nil)
	require.EqualError(t, err, "task is not claimed")
}
