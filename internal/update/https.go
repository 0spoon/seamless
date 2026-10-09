package update

import (
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"
)

// RequireHTTPS rejects a URL that would fetch over an unauthenticated
// channel. The installer `seamlessd update` fetches is piped to a shell, and
// the release list decides what that installer is told to install, so plain
// http means any router between here and the host can rewrite either.
func RequireHTTPS(raw string) error {
	u, err := neturl.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("refusing to fetch over %q: %s must be https", u.Scheme, raw)
	}
	return nil
}

// HTTPSOnlyRedirect is a CheckRedirect policy that refuses any redirect
// leaving https. Go's default client follows a downgrade silently, so without
// it the scheme check on the first URL covers only the first hop and a
// compromised or misconfigured host could bounce the fetch to plaintext.
func HTTPSOnlyRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return RequireHTTPS(req.URL.String())
}
