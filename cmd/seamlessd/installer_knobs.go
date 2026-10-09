package main

// installerKnobs are the environment variables seamlessd's unattended updater
// sets when it runs the installer that shipped with the release it moves to:
// docs/install on macOS and Linux, docs/install.ps1 on Windows.
//
// Append-only. Every knob is a permanent contract between two releases: the
// updater of the release that is running sets it, and the installer of the
// release being installed reads it. Installers stay in use long after they
// ship -- a rollback runs an older release's installer -- so a knob renamed or
// dropped here is broken for every release that already sets it, silently and
// on machines nobody is watching. Add a knob only together with the code that
// handles it in BOTH installers; installer_knobs_test.go holds both scripts to
// every entry.
var installerKnobs = []string{
	"SEAMLESS_VERSION",
	"SEAMLESS_INSTALL_DIR",
	"SEAMLESS_CHECKSUMS_SHA256",
	"SEAMLESS_CLIENT",
	"SEAMLESS_NO_HOOKS",
	"SEAMLESS_NO_ONBOARD_SKILL",
	"SEAMLESS_NO_RESEARCH_SKILL",
}
