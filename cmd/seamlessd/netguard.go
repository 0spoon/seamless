package main

import (
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// This file holds the two network-boundary guards from the 2026-07-19 audit:
// a Host-header allowlist (I1, DNS-rebinding) and the non-loopback bind warning
// (I6). They are a pair on purpose -- the allowlist is what makes widening the
// bind address survivable, and the warning is what tells the operator they just
// left the model the rest of the daemon assumes.

// loopbackHosts are the names and literals that always address this machine.
// A DNS-rebinding attacker controls the *name* the browser resolves, never the
// Host header it sends, so keeping the allowlist to these plus the configured
// bind host is what closes the hole: a request arriving as
// `Host: evil.example.com` cannot have come from a client that meant to talk to
// us, however the address resolved.
var loopbackHosts = map[string]bool{
	"127.0.0.1": true,
	"localhost": true,
	"::1":       true,
}

// hostGuard rejects requests whose Host header is not in the allowlist for this
// bind address, defeating DNS rebinding: a page on any origin can make the
// browser resolve a hostname to 127.0.0.1, but it cannot change the Host header
// that browser then sends, so the forged request never reaches a handler.
//
// The allowlist is the loopback names, plus a concrete bind host, plus extra --
// the names the operator has told us the daemon answers to (an advertised
// server URL, additional hostnames). A wildcard bind (0.0.0.0, ::, or a bare
// port) with nothing extra cannot have an allowlist -- the operator
// deliberately made the daemon reachable at addresses only they know -- so the
// guard steps aside there and warnNonLoopbackBind carries the message instead.
// Naming even one extra host is what turns the guard back on for a wildcard
// bind: the operator has now said what the daemon is called.
//
// The concrete bind host is in the list unconditionally, which is what lets
// `--addr 192.168.1.5:8081` work while still rejecting a rebound name.
func hostGuard(bind string, extra []string, next http.Handler) http.Handler {
	host := bindHost(bind)
	allowed := map[string]bool{}
	for _, h := range extra {
		if h = strings.ToLower(strings.Trim(strings.TrimSpace(h), "[]")); h != "" {
			allowed[h] = true
		}
	}
	if isWildcardHost(host) && len(allowed) == 0 {
		return next
	}
	if !isWildcardHost(host) {
		allowed[strings.ToLower(host)] = true
	}
	for h := range loopbackHosts {
		allowed[h] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[requestHost(r)] {
			// 421 is the status for "this server is not authoritative for the
			// requested host", which is exactly the claim being made.
			http.Error(w, "misdirected request: unrecognized Host header", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestHost normalizes r.Host to a bare lowercase hostname: port stripped,
// IPv6 brackets removed, so "[::1]:8081" and "::1" compare equal.
func requestHost(r *http.Request) string {
	h := r.Host
	if hostOnly, _, err := net.SplitHostPort(h); err == nil {
		h = hostOnly
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}

// bindHost extracts the host half of a bind address, tolerating a bare ":8081"
// (which means every interface, so it returns "" and reads as a wildcard).
func bindHost(bind string) string {
	if h, _, err := net.SplitHostPort(bind); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(bind, "[]")
}

// isWildcardHost reports whether a bind host means "every interface".
func isWildcardHost(h string) bool {
	switch h {
	case "", "0.0.0.0", "::", "[::]":
		return true
	}
	return false
}

// isLoopbackBind reports whether bind keeps the daemon reachable only from this
// machine. An unresolvable or wildcard host is reported as non-loopback, so the
// warning errs toward being shown.
func isLoopbackBind(bind string) bool {
	h := bindHost(bind)
	if isWildcardHost(h) {
		return false
	}
	if loopbackHosts[strings.ToLower(h)] {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// warnNonLoopbackBind logs a prominent warning when the daemon is about to
// listen somewhere other than loopback.
//
// This is a warning and not a refusal because binding wide is a legitimate
// choice (a container, a VM the operator reaches over a private network) and
// refusing it would only teach people to patch it out. What it must not be is
// *silent*: everything past this point -- a single static bearer key with no
// rate limiting, an event feed that streams the corpus -- is designed for a
// single-user localhost service, and none of it announces that at the moment
// the operator edits `addr:`.
//
// tlsOn is what the protection line is allowed to claim. The line used to say
// the cookie is not Secure and there is no TLS on this listener, which stops
// being true the moment tls.cert_file/tls.key_file are set -- and a security
// warning that states something false is worse than one that is missing,
// because the operator who checks it learns to stop reading it.
func warnNonLoopbackBind(bind string, tlsOn bool) {
	if isLoopbackBind(bind) {
		return
	}
	protection := "a single static bearer key sent in the clear; the console cookie is not Secure (no TLS on this listener)"
	advice := "set tls.cert_file/tls.key_file or keep it inside a trusted LAN; loopback plus an SSH tunnel remains the safest option"
	if tlsOn {
		protection = "TLS on this listener (the console cookie is Secure), guarded by a single static bearer key with no rate limiting"
		advice = "keep it inside a trusted LAN; the bearer key is the only thing between a LAN peer and the whole corpus"
	}
	slog.Warn("SECURITY: binding to a non-loopback address exposes Seamless beyond this machine",
		"addr", bind,
		"exposed", "MCP tools, hooks, and the console -- your entire memory corpus",
		"protection", protection,
		"advice", advice)
}

// warnAdvertisedLoopback warns when the daemon listens beyond this machine but
// the URL it hands out still names loopback.
//
// That combination is a half-finished widening, and it fails in two directions
// at once: every client the daemon tells where to find it (the agent card, the
// installed hooks, `seamlessd client-config`) is handed an address that means
// "your own machine", and the Host allowlist gets nothing to admit but loopback
// names, so a request arriving under the daemon's real name is refused with 421.
// Both symptoms are remote and neither looks like a config mistake from there.
func warnAdvertisedLoopback(bind, serverURL string) {
	if isLoopbackBind(bind) {
		return
	}
	u, err := url.Parse(serverURL)
	if err != nil {
		return
	}
	h := strings.ToLower(strings.Trim(u.Hostname(), "[]"))
	if !loopbackHosts[h] {
		ip := net.ParseIP(h)
		if ip == nil || !ip.IsLoopback() {
			return
		}
	}
	slog.Warn("server_url still points at this machine while the bind address does not",
		"addr", bind,
		"server_url", serverURL,
		"effect", "clients are handed a loopback URL, and the Host allowlist admits only loopback names (everything else gets 421)",
		"advice", "set server_url to the name or address other devices use to reach this daemon")
}
