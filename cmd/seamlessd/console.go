package main

import (
	"flag"
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
// 0600 temp file, and opens it in a browser -- pre-authenticating the session
// so the console loads without a manual key paste. By default it opens the OS
// default browser; --browser <app> targets a specific one (e.g. "Google
// Chrome", so an agent driving Chrome gets the auth cookie even when another
// browser is the default). It refuses to run if the key is unset or the server
// is unreachable, since the page POSTs to it.
func runConsoleOpen(args []string) error {
	fs := flag.NewFlagSet("console-open", flag.ContinueOnError)
	browser := fs.String("browser", "", `browser application to open instead of the default (e.g. "Google Chrome"; macOS only)`)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("seamlessd.console-open: %w", err)
	}
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
	if err := openInBrowser(f.Name(), *browser); err != nil {
		return fmt.Errorf("seamlessd.console-open: open browser: %w", err)
	}
	where := "default browser"
	if *browser != "" {
		where = *browser
	}
	fmt.Printf("opened pre-authenticated console at http://%s/console/ in %s\n", host, where)
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

// openInBrowser opens target (a file path or URL) in a browser: the OS default
// when app is empty, or the named browser application (macOS only).
func openInBrowser(target, app string) error {
	cmd, err := browserCommand(runtime.GOOS, target, app)
	if err != nil {
		return err
	}
	return cmd.Run()
}

// browserCommand builds the OS launch command for target. A non-empty app
// names a specific browser application; that is only expressible on macOS
// (`open -a`), so other platforms reject it rather than silently opening the
// default.
func browserCommand(goos, target, app string) (*exec.Cmd, error) {
	if app != "" && goos != "darwin" {
		return nil, fmt.Errorf("--browser is only supported on macOS (got GOOS=%s)", goos)
	}
	switch goos {
	case "darwin":
		if app != "" {
			return exec.Command("open", "-a", app, target), nil
		}
		return exec.Command("open", target), nil
	case "windows":
		return exec.Command("cmd", "/c", "start", "", target), nil
	default:
		return exec.Command("xdg-open", target), nil
	}
}
