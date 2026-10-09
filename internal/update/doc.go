// Package update is the background update check: it asks GitHub which
// Seamless releases exist, decides whether this install is behind, and keeps
// the one snapshot (Status) every surface renders -- the briefing notice, the
// console Updates section, doctor, and `seamlessd update --check`.
//
// It installs nothing. Applying an update stays the job of the release's own
// installer script, run by `seamlessd update` after its Sigstore bundle
// verifies (cmd/seamlessd/update.go), so there is one upgrade implementation.
//
// The package is a domain package that imports config and core only: the
// store, the event recorder and every OS read are injected (cmd/seamlessd
// wires them), which keeps each decision here a pure function of its inputs.
//
// Three rules hold everywhere in this package:
//
//   - Only release builds (main.distribution = "release", stamped by
//     goreleaser alone) check by default, write state, or count as an
//     installer install. A source build never touches GitHub unless its
//     owner sets update.check: true, and never writes state.
//   - Text shown to an agent or the owner is built from parsed versions and
//     the constant release URL, never from the release API's free text.
//   - update.check: false means no update traffic at all.
package update
