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
// top of cmd/seam/client.go consoleJSON). Neither binary can import the other:
// both are package main. This is the seamlessd-side copy of the FEATURE READ,
// kept deliberately small -- but the HTTP client under it is not a copy of
// anything: it is config.Config.HTTPClient, the one TLS trust decision both
// binaries share, because an install whose hooks refuse a certificate must
// never have had install-hooks accept it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/config"
)

// clientConsoleTimeout bounds the whole settings request. The console renders
// from the server's SQLite, so anything slower than this is a wedged daemon
// rather than a big page -- and an install must not hang on it. The separate
// connection deadline is config.DialTimeout, which fails a down or unreachable
// server fast on every surface that dials one.
const clientConsoleTimeout = 5 * time.Second

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
	client, err := cfg.HTTPClient(clientConsoleTimeout)
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
