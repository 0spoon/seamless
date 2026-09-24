package main

// The HTTP client itself is tested where it now lives
// (internal/config/httpclient_test.go): ca_file missing or unusable is an error,
// no ca_file leaves Go's default trust alone, and a private root verifies the
// server it was configured for. What is left to prove HERE is the wiring -- that
// the commands reach that constructor rather than one of their own, which is the
// only way the CLI could start trusting a certificate the rest of the install
// refuses.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
)

// A local misconfiguration reaches the operator as a local misconfiguration. If
// a command built its own client it would either skip the ca_file (and fail at
// the handshake, reading as an outage) or skip verification entirely; either way
// this message would not appear.
func TestCommandsUseTheSharedHTTPClient(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"status", []string{"status"}},
		{"version", []string{"version"}},
		{"doctor", []string{"doctor"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _, errb := stubEnv()
			e.loadConfig = func() (config.Config, error) {
				cfg := config.Defaults()
				cfg.TLS.CAFile = "/nonexistent/ca.pem"
				return cfg, nil
			}

			require.Equal(t, 1, dispatch(context.Background(), e, tc.argv))
			require.Contains(t, errb.String(), "tls.ca_file /nonexistent/ca.pem",
				"the command must surface config.Config.HTTPClient's local-config error")
		})
	}
}
