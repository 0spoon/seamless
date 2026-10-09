package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"regexp"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/arctop/seamless/internal/update"
)

// sigstoreTrustedRootJSON is a snapshot of the Sigstore public-good trusted
// root (Fulcio CA chain, Rekor transparency-log key, CT log keys), fetched
// from the Sigstore TUF repository and pinned here so verification is fully
// offline: no TUF refresh, no network beyond the asset fetches. The snapshot
// ages with the binary, and the binary updates itself, so each release
// re-pins a current root; if Sigstore ever rotates keys out from under an old
// binary, verification fails closed and a fresh install (whose installer
// still works, per its own cosign fallback rules) recovers.
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

	// releaseWorkflowTagIdentity is the certificate identity of this
	// repository's release workflow running on a tag, up to the tag's version.
	// releaseIdentityRegexp completes it with one exact version.
	releaseWorkflowTagIdentity = "https://github.com/" + update.Repo + "/.github/workflows/release.yml@refs/tags/v"
)

// releaseIdentityRegexp pins WHO may have signed an asset of release v: this
// repository's release workflow running on the tag vX.Y.Z itself, and nothing
// else. A signature from any other repo, workflow file or ref (a branch, a PR)
// fails even though it chains to the same Fulcio root -- and so does one the
// same workflow made for ANOTHER tag, which is the point of naming the tag:
// every release ships an install script and a checksums.txt signed the same
// way, so a pattern that accepted any v* tag would let an old signed installer
// or manifest be served as the one for v, and the updater would install
// whatever that older manifest describes.
//
// One org only. The updater verifies the release it moves to and the release
// it would roll back to, and both carry checksums.txt.sigstore.json, which
// only releases cut after the move to arctop do (plan:move-to-arctop). The
// installers are different: docs/install and docs/install.ps1 verify the
// checksums.txt of whatever version they are asked for, which can predate the
// move, so they accept the pre-move signer as well.
func releaseIdentityRegexp(v update.Version) string {
	return "^" + regexp.QuoteMeta(releaseWorkflowTagIdentity+v.String()) + "$"
}

// sigstoreTrustedRoot parses the embedded trusted-root snapshot.
func sigstoreTrustedRoot() (*root.TrustedRoot, error) {
	tr, err := root.NewTrustedRootFromJSON(sigstoreTrustedRootJSON)
	if err != nil {
		return nil, fmt.Errorf("parse embedded sigstore trusted root: %w", err)
	}
	return tr, nil
}

// verifyReleaseAsset checks that artifact is the exact asset the Sigstore
// bundle in bundleJSON attests, signed by this repository's release workflow
// on the tag of release v (releaseIdentityRegexp). It is the gate between
// "bytes fetched over HTTPS" and "bytes the updater acts on": TLS
// authenticates the host that served them, this proves they came out of the
// release pipeline for exactly that release. The updater runs it on a
// release's install script before piping it to a shell, and on its
// checksums.txt before pinning the installer to it.
func verifyReleaseAsset(trusted root.TrustedMaterial, bundleJSON, artifact []byte, v update.Version) error {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return fmt.Errorf("parse sigstore bundle: %w", err)
	}
	return verifyReleaseEntity(trusted, &b, artifact, v)
}

// verifyReleaseEntity is verifyReleaseAsset after bundle parsing, split so
// tests can drive the exact production verifier configuration and identity
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
func verifyReleaseEntity(trusted root.TrustedMaterial, entity verify.SignedEntity, artifact []byte, v update.Version) error {
	verifier, err := verify.NewVerifier(trusted,
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1))
	if err != nil {
		return fmt.Errorf("build sigstore verifier: %w", err)
	}
	identity, err := verify.NewShortCertificateIdentity(signingIssuer, "", "", releaseIdentityRegexp(v))
	if err != nil {
		return fmt.Errorf("build signing identity policy: %w", err)
	}
	if _, err := verifier.Verify(entity, verify.NewPolicy(
		verify.WithArtifact(bytes.NewReader(artifact)),
		verify.WithCertificateIdentity(identity))); err != nil {
		return fmt.Errorf("sigstore verification for v%s: %w", v, err)
	}
	return nil
}
