package main

import (
	"bytes"
	_ "embed"
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// sigstoreTrustedRootJSON is a snapshot of the Sigstore public-good trusted
// root (Fulcio CA chain, Rekor transparency-log key, CT log keys), fetched
// from the Sigstore TUF repository and pinned here so verification is fully
// offline: no TUF refresh, no network beyond the two asset fetches. The
// snapshot ages with the binary, and the binary updates itself, so each
// release re-pins a current root; if Sigstore ever rotates keys out from
// under an old binary, verification fails closed and a fresh install (whose
// installer still works, per its own cosign fallback rules) recovers.
//
// Refresh the snapshot with:
//
//	cosign trusted-root create --with-default-services --out cmd/seamlessd/sigstore_trusted_root.json
//
//go:embed sigstore_trusted_root.json
var sigstoreTrustedRootJSON []byte

const (
	// signingIssuer is the OIDC issuer that vouched for the signing identity:
	// GitHub Actions' token service, which is what authenticates "this
	// certificate was minted for a workflow run" during keyless signing.
	signingIssuer = "https://token.actions.githubusercontent.com"

	// signingIdentityRegexp pins WHO may have signed the installer: this
	// repository's release workflow running on a version tag, and nothing
	// else. It is the same identity docs/install and docs/install.ps1 pin
	// when they verify checksums.txt, so the whole supply chain enforces one
	// identity contract. A signature from any other repo, workflow file, or
	// ref (a branch, a PR) fails the policy even though it chains to the
	// same Fulcio root.
	signingIdentityRegexp = `^https://github\.com/0spoon/seamless/\.github/workflows/release\.yml@refs/tags/v`
)

// sigstoreTrustedRoot parses the embedded trusted-root snapshot.
func sigstoreTrustedRoot() (*root.TrustedRoot, error) {
	tr, err := root.NewTrustedRootFromJSON(sigstoreTrustedRootJSON)
	if err != nil {
		return nil, fmt.Errorf("parse embedded sigstore trusted root: %w", err)
	}
	return tr, nil
}

// verifyInstallerBundle checks that script is the exact artifact attested by
// the Sigstore bundle in bundleJSON, signed by this repository's release
// workflow. It is the gate between "bytes fetched over HTTPS" and "bytes
// piped to a shell": TLS authenticates the host that served the script, this
// proves the script itself came out of the pinned release pipeline.
func verifyInstallerBundle(trusted root.TrustedMaterial, bundleJSON, script []byte) error {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return fmt.Errorf("parse sigstore bundle: %w", err)
	}
	return verifyInstallerEntity(trusted, &b, script)
}

// verifyInstallerEntity is verifyInstallerBundle after bundle parsing, split
// so tests can drive the exact production verifier configuration and identity
// policy with a virtual signing infrastructure (sigstore-go's testing CA
// produces SignedEntity values directly, not bundle JSON).
//
// The verifier requires the signature to appear in a transparency log and to
// carry an observed timestamp (the log's signed entry timestamp), both checked
// against the embedded trusted root -- so a leaked signing certificate alone,
// without a public log entry, does not verify. Certificate SCTs are not
// required: the transparency-log requirement is the accountability mechanism
// here, and requiring SCTs would put the production path beyond what the
// virtual test CA can exercise.
func verifyInstallerEntity(trusted root.TrustedMaterial, entity verify.SignedEntity, script []byte) error {
	verifier, err := verify.NewVerifier(trusted,
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1))
	if err != nil {
		return fmt.Errorf("build sigstore verifier: %w", err)
	}
	identity, err := verify.NewShortCertificateIdentity(signingIssuer, "", "", signingIdentityRegexp)
	if err != nil {
		return fmt.Errorf("build signing identity policy: %w", err)
	}
	if _, err := verifier.Verify(entity, verify.NewPolicy(
		verify.WithArtifact(bytes.NewReader(script)),
		verify.WithCertificateIdentity(identity))); err != nil {
		return fmt.Errorf("sigstore verification: %w", err)
	}
	return nil
}
