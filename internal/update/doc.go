// Package update is the background update check and the daemon's side of
// automatic updates: it asks GitHub which Seamless releases exist, decides
// whether this install is behind, and keeps the one snapshot (Status) every
// surface renders -- the briefing notice, the console Updates section, doctor,
// and `seamlessd update --check`.
//
// It never installs anything in-process. On an install the installer made,
// with an updater wired (Deps.Spawner) and the owner's permission
// (update.auto), it decides which release to take (Target: soaked by GitHub's
// clock, not blocked or held) and when (Decide: idle, or a lull once the
// max_defer deadline has passed), then hands one attempt to an updater
// process, `seamlessd update --auto`, which verifies the release and runs its
// own installer script -- the same one `seamlessd update` runs, so there is
// one upgrade implementation. The updater reports through attempt.json
// (attempt.go); the checker folds that record into blocks, backoff, a hold
// after a deliberate downgrade, and a pause after repeated rollbacks
// (fold.go). Every other install is told about new releases and nothing more.
//
// The package is a domain package that imports config and core only: the
// store, the event recorder, the spawner, the request tracker and every OS
// read are injected (cmd/seamlessd wires them), which keeps each decision
// here a pure function of its inputs.
//
// Three rules hold everywhere in this package:
//
//   - Only release builds (main.distribution = "release", stamped by
//     goreleaser alone) check by default, write state, or count as an
//     installer install. A source build never touches GitHub unless its
//     owner sets update.check: true, and never writes state.
//   - Text shown to an agent or the owner is built from parsed versions, the
//     constant release URL and fixed words, never from the release API's
//     free text. An updater's own error summary reaches the owner (console,
//     doctor) but never a notice or an event payload.
//   - update.check: false means no update traffic at all, and so no
//     automatic update and no Update now either.
package update
