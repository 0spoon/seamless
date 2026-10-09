package main

// Build metadata for binaries no ldflags stamped.
//
// The Makefile and goreleaser stamp version/commit/buildDate with -X. A
// `go install github.com/arctop/seamless/cmd/...@vX.Y.Z` (or a plain
// `go build`) stamps nothing, and would otherwise call itself 0.0.0-dev
// everywhere the version surfaces: `seamlessd version`, /healthz, the MCP
// handshake, and `seamlessd update --check`. The Go toolchain records what it
// knows in the binary's build info, so an unstamped build reads it from there.

import (
	"runtime/debug"
	"strings"
)

// The in-source values of version, commit and buildDate. main.go initializes
// the variables from these constants (which -X still overrides) so the
// "nothing was stamped" test below cannot drift from the defaults.
const (
	devVersion       = "0.0.0-dev"
	unknownBuildInfo = "unknown"
)

// shortCommitLen matches the short SHA the stamped routes use
// (`git rev-parse --short HEAD` in the Makefile, .ShortCommit in goreleaser),
// so a build-info commit reads the same as a stamped one.
const shortCommitLen = 7

// buildMeta is the version, commit and build date `seamlessd version` prints.
type buildMeta struct {
	version string
	commit  string
	date    string
}

// unstampedMeta is the triple a build carries when no -X flag touched it.
var unstampedMeta = buildMeta{version: devVersion, commit: unknownBuildInfo, date: unknownBuildInfo}

// applyBuildInfo resolves version, commit and buildDate from the binary's build
// info. main calls it first, before anything reads them.
func applyBuildInfo() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		info = nil
	}
	m := resolveBuildMeta(buildMeta{version: version, commit: commit, date: buildDate}, info)
	version, commit, buildDate = m.version, m.commit, m.date
}

// resolveBuildMeta returns the build metadata to report, given what ldflags
// left in the variables and the binary's build info (nil when unavailable).
//
// Any stamp wins wholesale. Build info is consulted only when all three values
// are still the in-source defaults: a stamped 0.0.0-dev is the Makefile's
// deliberate answer off a tag (paired with the commit it stamps alongside),
// and is indistinguishable from the in-source default by value. The build info
// does record -ldflags, but not under -trimpath, which goreleaser uses, so it
// cannot tell the two apart either.
//
// For an unstamped build:
//
//   - version is Main.Version minus the leading "v" ("v0.6.0" -> "0.6.0"); "(devel)"
//     (go test, -buildvcs=false) and "" keep 0.0.0-dev. A pseudo-version from an
//     untagged commit is kept: it is the toolchain's own name for that commit,
//     and update's parseVersion rejects its "-" as not a published release. Any
//     "+..." build suffix is dropped, because buildVersion appends "+commit" and
//     seam's `version` cuts the daemon's version at the first "+".
//   - a modified checkout (vcs.modified=true) appends "-dirty" to that version,
//     the `git describe --dirty` convention. A dirty build at a tag therefore
//     reads "0.6.0-dirty", never the clean "0.6.0" that would tell update it is
//     the published release; commit stays a plain SHA usable as a git ref.
//   - commit is vcs.revision cut to shortCommitLen.
//   - date is vcs.time, the commit time: for a clean build it dates exactly the
//     source in the binary (the reproducible-build source date, which goreleaser
//     also uses for mod_timestamp). A dirty build keeps "unknown": its code is
//     newer than that commit, and the toolchain records no build time.
//
// A module-proxy `go install ...@vX.Y.Z` records no vcs settings, so only the
// version resolves there.
func resolveBuildMeta(stamped buildMeta, info *debug.BuildInfo) buildMeta {
	if stamped != unstampedMeta || info == nil {
		return stamped
	}

	var revision, vcsTime string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			vcsTime = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}

	out := stamped
	if v := moduleVersion(info.Main.Version); v != "" {
		out.version = v
		if modified {
			out.version += "-dirty"
		}
	}
	if revision != "" {
		out.commit = revision[:min(len(revision), shortCommitLen)]
	}
	if vcsTime != "" && !modified {
		out.date = vcsTime
	}
	return out
}

// moduleVersion turns a build-info module version into the form the stamped
// routes use: no leading "v" and no "+..." build suffix. It returns "" for a
// version the toolchain did not know ("(devel)" or empty).
func moduleVersion(v string) string {
	if v == "" || v == "(devel)" {
		return ""
	}
	v, _, _ = strings.Cut(v, "+")
	return strings.TrimPrefix(v, "v")
}
