package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Canonical installer endpoints. The release-fetch + checksum + binary-swap +
// service-rewire logic lives in exactly two published scripts -- docs/install
// (POSIX) and docs/install.ps1 (PowerShell), served verbatim at
// thereisnospoon.org. `seamlessd update` deliberately does NOT reimplement any of
// it: it fetches the script for this OS and runs it, the same path a fresh
// install takes, so there is ONE upgrade implementation to keep correct. (These
// are also the URLs baked into service.go's install hints and the two installers'
// own headers; keep them in step if the site ever moves.)
const (
	installerURLUnix    = "https://thereisnospoon.org/install"
	installerURLWindows = "https://thereisnospoon.org/install.ps1"
	// githubRepo is where releases live; the installer scripts hardcode the same
	// "0spoon/seamless". Used only by --check to read the latest release tag.
	githubRepo = "0spoon/seamless"
)

// updatePlan is the OS-specific way to run the canonical installer: fetch URL
// over HTTPS and feed it to Prog, which reads the script from stdin. Like
// serviceControlPlan it is a pure value so the argv can be asserted in tests
// without fetching or executing anything.
type updatePlan struct {
	OS       string   // GOOS this plan targets
	URL      string   // installer script fetched over HTTPS
	Prog     string   // interpreter that runs the fetched script from stdin
	ProgArgs []string // interpreter args; the script itself arrives on stdin
	RunHint  string   // the equivalent hand-run one-liner, shown for transparency
}

// updatePlanFor builds the plan for goos. darwin/linux run the POSIX installer
// through `sh -s` (read program from stdin); Windows runs the PowerShell
// installer through `powershell ... -Command -` (same). Both mirror the two
// documented install one-liners, so update reuses the exact path a fresh install
// takes -- including the Windows running-exe swap, which stays in the .ps1.
func updatePlanFor(goos string) updatePlan {
	if goos == "windows" {
		return updatePlan{
			OS:       goos,
			URL:      installerURLWindows,
			Prog:     "powershell",
			ProgArgs: []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "-"},
			RunHint:  installRunHint(goos, installerURLWindows),
		}
	}
	return updatePlan{
		OS:       goos,
		URL:      installerURLUnix,
		Prog:     "sh",
		ProgArgs: []string{"-s"},
		RunHint:  installRunHint(goos, installerURLUnix),
	}
}

// installRunHint is the equivalent hand-run one-liner for fetching and running
// the installer at url on goos -- shown for transparency, and kept honest when
// --url overrides the default endpoint.
func installRunHint(goos, url string) string {
	if goos == "windows" {
		return "irm " + url + " | iex"
	}
	return "curl -fsSL " + url + " | sh"
}

// runUpdate upgrades Seamless in place to the latest published release by
// re-running the canonical installer for this OS. The installer's env knobs are
// inherited by the child, so `SEAMLESS_VERSION=0.3.0 seamlessd update` pins a
// version and `SEAMLESS_INSTALL_DIR=... seamlessd update` retargets, exactly as
// the curl installer does. --check only reports installed vs latest; --dry-run
// prints what would run without fetching or executing.
func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "report installed vs latest release version and exit without changing anything")
	dryRun := fs.Bool("dry-run", false, "print what would run and exit without fetching or executing")
	urlFlag := fs.String("url", "", "override the installer URL (default: the canonical thereisnospoon.org installer for this OS)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *check {
		return reportUpdateCheck(os.Stdout)
	}

	plan := updatePlanFor(runtime.GOOS)
	if u := strings.TrimSpace(*urlFlag); u != "" {
		plan.URL = u
		plan.RunHint = installRunHint(plan.OS, u)
	}

	fmt.Printf("\n%s %s\n", bold("Seamless"), dim("update"+dryRunTag(*dryRun)))
	fieldRow("source", plan.URL)
	fieldRow("run", dim(plan.RunHint))

	if *dryRun {
		fmt.Printf("%s%s\n", fieldCont, dim("no changes made -- re-run without --dry-run to update"))
		return nil
	}

	script, err := fetchInstaller(plan.URL)
	if err != nil {
		return fmt.Errorf("seamlessd.update: %w", err)
	}

	fmt.Printf("\n%s\n", dim("running the installer..."))
	cmd := exec.Command(plan.Prog, plan.ProgArgs...)
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ() // pass SEAMLESS_* knobs through to the installer
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("seamlessd.update: installer failed (equivalent to: %s): %w", plan.RunHint, err)
	}
	return nil
}

// fetchInstaller downloads the installer script over HTTPS. This is the ONLY
// network fetch update performs itself -- a single GET of a small bootstrap
// script, NOT the release archive; the archive download, its checksum
// verification, and the binary swap all stay inside the script this returns.
func fetchInstaller(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch installer %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch installer %s: unexpected status %s", url, resp.Status)
	}
	// Installers are a few KB; 1 MiB is generous and caps a misrouted response.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read installer %s: %w", url, err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return "", fmt.Errorf("fetch installer %s: empty response", url)
	}
	return string(body), nil
}

// githubRelease is the sliver of the GitHub releases API that --check reads.
type githubRelease struct {
	TagName string `json:"tag_name"`
}

// reportUpdateCheck fetches the latest published release tag and compares it to
// the running build, printing a one-line verdict. It changes nothing.
func reportUpdateCheck(w io.Writer) error {
	latest, err := latestReleaseTag(fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo))
	if err != nil {
		return fmt.Errorf("seamlessd.update: %w", err)
	}

	fmt.Fprintf(w, "\n%s %s\n", bold("Seamless"), dim("update --check"))
	fieldRow("current", buildVersion())
	fieldRow("latest", latest)

	// Compare on the base version; buildVersion() only adds +commit for display.
	cmp, ok := compareReleases(version, latest)
	switch {
	case !ok:
		fieldRow("status", yellow("development build")+dim(" -- 'seamlessd update' installs the latest release"))
	case cmp < 0:
		fieldRow("status", green("update available")+dim(" -- run 'seamlessd update' to upgrade"))
	case cmp == 0:
		fieldRow("status", dim("up to date"))
	default:
		fieldRow("status", dim("ahead of the latest published release"))
	}
	return nil
}

// latestReleaseTag returns the tag_name of the repo's latest published release.
func latestReleaseTag(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("query latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("query latest release: unexpected status %s "+
			"(GitHub API rate limit?); pin a version and run 'seamlessd update' instead", resp.Status)
	}
	var rel githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return "", fmt.Errorf("decode latest release: %w", err)
	}
	tag := strings.TrimSpace(rel.TagName)
	if tag == "" {
		return "", fmt.Errorf("latest release has no tag_name")
	}
	return tag, nil
}

// compareReleases compares two versions by their numeric major.minor.patch,
// returning -1/0/1 (a<b / a==b / a>b). ok is false when either side is not a
// clean published release -- the 0.0.0-dev sentinel, a goreleaser snapshot, or
// anything unparseable -- in which case the numeric result is meaningless.
func compareReleases(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range 3 {
		switch {
		case av[i] < bv[i]:
			return -1, true
		case av[i] > bv[i]:
			return 1, true
		}
	}
	return 0, true
}

// parseVersion extracts [major, minor, patch] from a version string, tolerating a
// leading "v". A pre-release ("-...") or build ("+...") suffix means this is not a
// clean published release (the 0.0.0-dev sentinel, a 0.3.4-SNAPSHOT-<sha>
// goreleaser build, an -rc), so it reports ok=false rather than compare a partial
// number and call a dev build "up to date".
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if strings.ContainsAny(s, "+-") {
		return [3]int{}, false
	}
	fields := strings.Split(s, ".")
	if len(fields) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return [3]int{}, false // zero array whenever ok is false
		}
		out[i] = n
	}
	return out, true
}
