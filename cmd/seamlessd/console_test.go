package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBrowserHost(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8081": "127.0.0.1:8081",
		"0.0.0.0:8081":   "127.0.0.1:8081",
		":8081":          "127.0.0.1:8081",
		"[::]:8081":      "127.0.0.1:8081",
		"localhost:9000": "localhost:9000",
		"not-hostport":   "not-hostport", // handed back verbatim
	}
	for in, want := range cases {
		require.Equal(t, want, browserHost(in), "browserHost(%q)", in)
	}
}

func TestRenderConsoleLoginPage(t *testing.T) {
	page, err := renderConsoleLoginPage("127.0.0.1:8081", "deadbeefKEY")
	require.NoError(t, err)
	// POSTs the key to the login endpoint and auto-submits.
	require.Contains(t, page, `action="http://127.0.0.1:8081/console/login"`)
	require.Contains(t, page, `name="key" value="deadbeefKEY"`)
	require.Contains(t, page, `name="next" value="/console/"`)
	require.Contains(t, page, `.submit();`)
}

func TestBrowserCommand(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		app      string
		wantArgs []string
		wantErr  bool
	}{
		{"darwin default", "darwin", "", []string{"open", "/tmp/x.html"}, false},
		{"darwin named app", "darwin", "Google Chrome", []string{"open", "-a", "Google Chrome", "/tmp/x.html"}, false},
		{"linux default", "linux", "", []string{"xdg-open", "/tmp/x.html"}, false},
		{"windows default", "windows", "", []string{"cmd", "/c", "start", "", "/tmp/x.html"}, false},
		{"linux named app rejected", "linux", "firefox", nil, true},
		{"windows named app rejected", "windows", "chrome", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := browserCommand(tt.goos, "/tmp/x.html", tt.app)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantArgs, cmd.Args)
		})
	}
}

func TestRenderConsoleLoginPage_EscapesKey(t *testing.T) {
	// A key with attribute-breaking characters must be contextually escaped so
	// it cannot terminate the value="" attribute or inject markup.
	page, err := renderConsoleLoginPage("127.0.0.1:8081", `a"><script>x`)
	require.NoError(t, err)
	require.NotContains(t, page, `<script>x`)
	require.NotContains(t, page, `value="a">`)
	require.True(t, strings.Contains(page, "&#34;") || strings.Contains(page, "&quot;"),
		"quote in key must be escaped, got: %s", page)
}
