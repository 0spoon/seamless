package main

// Reading the effective optional-feature state from a REMOTE daemon, for an
// install on a client machine.
//
// effectiveFeatures (install_hooks.go) answers the same question by opening the
// local database, which is exactly what a client does not have: no data dir, no
// seam.db, no stored override row. The state still matters here, because skills
// are written into the agent client's config directory on THIS machine and a
// disabled feature's skill must not be installed -- so the answer comes over the
// wire from the daemon that owns it.
//
// The same logic exists in `seam doctor` (cmd/seam/doctor.go consoleFeatures on
// top of cmd/seam/client.go consoleJSON and cmd/seam/env.go httpClient). Neither
// binary can import the other: both are package main. This is the seamlessd-side
// copy, kept deliberately small.

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/config"
)

const (
	// clientConsoleTimeout bounds the whole settings request. The console
	// renders from the server's SQLite, so anything slower than this is a wedged
	// daemon rather than a big page -- and an install must not hang on it.
	clientConsoleTimeout = 5 * time.Second
	// clientDialTimeout fails a down or unreachable server fast, separately from
	// the response deadline.
	clientDialTimeout = 3 * time.Second
)

// clientFeatures resolves the optional-feature config a client install wires
// its skills against: the server's effective state (file/env base plus the
// console's stored override), read from its console settings JSON.
//
// Failure-soft, exactly like effectiveFeatures: an unreachable or unreadable
// server WARNS and falls back to this machine's file/env base. It never returns
// a silent guess -- "every optional feature off" would be a plausible dummy that
// deletes the owner's installed skills on a server that has them switched on.
func clientFeatures(cfg config.Config, baseURL string) config.Features {
	feats, err := clientConsoleFeatures(cfg, baseURL)
	if err != nil {
		fmt.Printf("%s cannot read the feature toggles from %s (%v)\n%s%s\n",
			yellow("warning:"), baseURL, err, fieldCont, dim("using this machine's file/env features config"))
		return cfg.Features
	}
	return *feats
}

// clientConsoleFeatures fetches the daemon's effective optional-feature state
// from the console settings JSON.
//
// The decoded field is a POINTER on purpose: a daemon that predates the
// features contract answers without it, and decoding an absent object into a
// value would yield "every optional feature off" -- a plausible dummy the caller
// cannot tell from a real answer. Absent has to stay distinguishable from off,
// so it becomes an error and the caller degrades loudly.
func clientConsoleFeatures(cfg config.Config, baseURL string) (*config.Features, error) {
	var data struct {
		FeaturesConfig *config.Features `json:"featuresConfig"`
	}
	if err := clientConsoleJSON(cfg, baseURL, "/console/settings?format=json", &data); err != nil {
		return nil, err
	}
	if data.FeaturesConfig == nil {
		return nil, errors.New("settings JSON carries no featuresConfig (is the server older than this client?)")
	}
	return data.FeaturesConfig, nil
}

// clientConsoleJSON performs one authenticated GET against the server's console
// JSON surface and decodes the body into v.
func clientConsoleJSON(cfg config.Config, baseURL, path string, v any) error {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.MCP.APIKey)
	req.Header.Set("Accept", "application/json")
	client, err := clientHTTPClient(cfg)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("unreachable at %s: %w", baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("console returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("unreadable response from %s: %w", path, err)
	}
	return nil
}

// clientHTTPClient builds the HTTP client this install uses to reach the server,
// adding tls.ca_file to the system trust pool so a private-CA or self-signed
// https server verifies -- the same trust decision cmd/seam/env.go's httpClient
// makes, because a client whose install-hooks trusts a certificate its hooks
// then reject is worse than one that fails outright.
//
// A configured-but-unusable tls.ca_file is an ERROR, never a silent fall back to
// the system pool: the request would then fail in the TLS handshake and read as
// an outage, which is precisely the local-vs-remote distinction AGENTS.md
// requires be kept (llm-degradation-remote-vs-local).
func clientHTTPClient(cfg config.Config) (*http.Client, error) {
	tr := &http.Transport{
		Proxy:       http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{Timeout: clientDialTimeout}).DialContext,
	}
	if ca := strings.TrimSpace(cfg.TLS.CAFile); ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, fmt.Errorf("tls.ca_file %s: %w", ca, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			// Windows has historically returned an error here; an empty pool plus
			// the configured root is still a working trust store for the one
			// server this install talks to.
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tls.ca_file %s: no PEM certificate found", ca)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Timeout: clientConsoleTimeout, Transport: tr}, nil
}
