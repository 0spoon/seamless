package main

import (
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/config"
)

// consoleLoginTmpl is a one-shot, self-submitting login page. Opened from a
// local file:// URL, it POSTs the static key to the console's /console/login
// endpoint, which sets the HttpOnly session cookie and 303-redirects into the
// console -- so `make console` lands on an authenticated page with no paste.
// The cookie is SameSite=Lax; setting it on this cross-site POST is allowed,
// and the follow-up top-level GET to /console/ carries it. Rendered with
// html/template so the key is contextually escaped inside the attribute.
var consoleLoginTmpl = template.Must(template.New("console-login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="robots" content="noindex">
<title>Signing in to Seamless...</title>
</head>
<body>
<p>Signing in to the Seamless console...</p>
<form id="login" method="post" action="http://{{.Addr}}/console/login">
<input type="hidden" name="key" value="{{.Key}}">
<input type="hidden" name="next" value="/console/">
<noscript><button type="submit">Continue to console</button></noscript>
</form>
<script>document.getElementById("login").submit();</script>
</body>
</html>
`))

// runConsoleOpen renders a self-submitting console login page, writes it to a
// 0600 temp file, and opens it in the default browser -- pre-authenticating the
// session so the console loads without a manual key paste. It refuses to run if
// the key is unset or the server is unreachable, since the page POSTs to it.
func runConsoleOpen(_ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("seamlessd.console-open: %w", err)
	}
	if strings.TrimSpace(cfg.MCP.APIKey) == "" {
		return fmt.Errorf("seamlessd.console-open: mcp.api_key is empty; set it in seamless.yaml first")
	}
	host := browserHost(cfg.Addr)
	if !serverReachable(host) {
		return fmt.Errorf("seamlessd.console-open: console not reachable at http://%s -- start it with `make run` or `make start-service`", host)
	}

	page, err := renderConsoleLoginPage(host, cfg.MCP.APIKey)
	if err != nil {
		return fmt.Errorf("seamlessd.console-open: %w", err)
	}
	f, err := os.CreateTemp("", "seamless-console-*.html")
	if err != nil {
		return fmt.Errorf("seamlessd.console-open: temp file: %w", err)
	}
	// CreateTemp makes the file 0600, so the embedded key stays owner-readable.
	if _, err := f.WriteString(page); err != nil {
		_ = f.Close()
		return fmt.Errorf("seamlessd.console-open: write login page: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("seamlessd.console-open: close login page: %w", err)
	}
	if err := openInBrowser(f.Name()); err != nil {
		return fmt.Errorf("seamlessd.console-open: open browser: %w", err)
	}
	fmt.Printf("opened pre-authenticated console at http://%s/console/\n", host)
	return nil
}

// renderConsoleLoginPage returns the self-submitting login HTML for addr+key.
func renderConsoleLoginPage(addr, key string) (string, error) {
	var b strings.Builder
	if err := consoleLoginTmpl.Execute(&b, struct{ Addr, Key string }{Addr: addr, Key: key}); err != nil {
		return "", err
	}
	return b.String(), nil
}

// browserHost turns a bind address into one a browser can reach: a wildcard or
// unspecified host (":8081", "0.0.0.0:8081", "[::]:8081") maps to loopback.
func browserHost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr // not host:port; hand it back verbatim
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// serverReachable reports whether the console answers /healthz within 2s. Any
// HTTP response (200 or a 503 degraded) counts; only a transport error is down.
func serverReachable(host string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + host + "/healthz")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// openInBrowser opens target (a file path or URL) in the OS default browser.
func openInBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Run()
}
