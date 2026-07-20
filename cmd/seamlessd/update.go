package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Canonical installer delivery. The release-fetch + checksum + binary-swap +
// service-rewire logic lives in exactly two published scripts -- docs/install
// (POSIX) and docs/install.ps1 (PowerShell). Humans run them from
// thereisnospoon.org (service.go's install hints and the docs one-liners);
// `seamlessd update` instead fetches the byte-identical copies published as
// GitHub release assets, because those ship atomically with the Sigstore
// bundles the release workflow signs them with (release.yml), and update
// verifies script against bundle before piping anything to a shell -- TLS
// authenticates the host, the bundle proves the bytes came out of this repo's
// release pipeline (audit M3). update deliberately does NOT reimplement any
// install logic: after verification it runs the same script a fresh install
// runs, so there is ONE upgrade implementation to keep correct.
const (
	// githubRepo is where releases live; the installer scripts hardcode the same
	// "0spoon/seamless". Also used by --check to read the latest release tag.
	githubRepo = "0spoon/seamless"
	// releaseDownloadBase resolves to the newest published release's assets.
	releaseDownloadBase = "https://github.com/" + githubRepo + "/releases/latest/download"
)

// updatePlan is the OS-specific way to run the canonical installer: fetch URL
// over HTTPS and feed it to Prog, which reads the script from stdin. Like
// serviceControlPlan it is a pure value so the argv can be asserted in tests
// without fetching or executing anything.
type updatePlan struct {
	OS        string   // GOOS this plan targets
	URL       string   // installer script fetched over HTTPS
	BundleURL string   // Sigstore bundle attesting the script; empty = unverifiable (custom --url)
	Prog      string   // interpreter that runs the fetched script from stdin
	ProgArgs  []string // interpreter args; the script itself arrives on stdin
	RunHint   string   // the equivalent hand-run one-liner, shown for transparency
}

// updatePlanFor builds the plan for goos. darwin/linux run the POSIX installer
// through `sh -s` (read program from stdin); Windows runs the PowerShell
// installer through `powershell ... -Command -` (same). Both mirror the two
// documented install one-liners, so update reuses the exact path a fresh install
// takes -- including the Windows running-exe swap, which stays in the .ps1.
func updatePlanFor(goos string) updatePlan {
	if goos == "windows" {
		return updatePlan{
			OS:        goos,
			URL:       releaseDownloadBase + "/install.ps1",
			BundleURL: releaseDownloadBase + "/install.ps1.sigstore.json",
			Prog:      "powershell",
			ProgArgs:  []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "-"},
			RunHint:   installRunHint(goos, releaseDownloadBase+"/install.ps1"),
		}
	}
	return updatePlan{
		OS:        goos,
		URL:       releaseDownloadBase + "/install",
		BundleURL: releaseDownloadBase + "/install.sigstore.json",
		Prog:      "sh",
		ProgArgs:  []string{"-s"},
		RunHint:   installRunHint(goos, releaseDownloadBase+"/install"),
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
// re-running the canonical installer for this OS, after verifying the
// script's Sigstore bundle against the release-workflow identity compiled
// into this binary (update_verify.go) -- verification failure is fatal, with
// no fallback. A custom --url has no bundle to verify, so it runs TLS-only
// with a printed warning. The installer's env knobs are inherited by the
// child, so `SEAMLESS_VERSION=0.3.0 seamlessd update` pins a version and
// `SEAMLESS_INSTALL_DIR=... seamlessd update` retargets, exactly as the curl
// installer does. --check only reports installed vs latest; --dry-run prints
// what would run without fetching or executing.
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
		plan.BundleURL = "" // no signed bundle rides alongside a custom endpoint
		plan.RunHint = installRunHint(plan.OS, u)
	}

	fmt.Printf("\n%s %s\n", bold("Seamless"), dim("update"+dryRunTag(*dryRun)))
	fieldRow("source", plan.URL)
	if plan.BundleURL != "" {
		fieldRow("signature", dim("sigstore bundle, signed by this repo's release workflow"))
	} else {
		fieldRow("signature", yellow("none")+dim(" -- custom --url carries no sigstore bundle; https is the only authentication"))
	}
	fieldRow("run", dim(plan.RunHint))

	if *dryRun {
		fmt.Printf("%s%s\n", fieldCont, dim("no changes made -- re-run without --dry-run to update"))
		return nil
	}

	script, err := fetchInstaller(plan.URL)
	if err != nil {
		return fmt.Errorf("seamlessd.update: %w", missingAssetHint(err))
	}
	if plan.BundleURL != "" {
		bundleJSON, err := fetchInstaller(plan.BundleURL)
		if err != nil {
			return fmt.Errorf("seamlessd.update: %w", missingAssetHint(err))
		}
		trusted, err := sigstoreTrustedRoot()
		if err != nil {
			return fmt.Errorf("seamlessd.update: %w", err)
		}
		if err := verifyInstallerBundle(trusted, []byte(bundleJSON), []byte(script)); err != nil {
			return fmt.Errorf("seamlessd.update: refusing to run %s: %w", plan.URL, err)
		}
		fmt.Printf("%s%s\n", fieldCont, green("signature verified")+dim(" -- release workflow identity on a version tag"))
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

// fetchInstaller downloads one small release asset over HTTPS -- the
// installer script or its Sigstore bundle, NOT the release archive; the
// archive download, its checksum verification, and the binary swap all stay
// inside the script this returns.
//
// The script comes back to be piped straight into sh/powershell, which makes
// this the one place where remote bytes become locally executing code. So the
// transport is not merely preferred-HTTPS, it is required-HTTPS, on the
// initial URL (--url can name anything) and on every redirect hop: a 302 from
// https to http would otherwise hand the whole script to whoever is on the
// wire. On the default (non---url) path the fetched script must additionally
// survive verifyInstallerBundle before it runs.
func fetchInstaller(url string) (string, error) {
	return fetchInstallerWith(httpsOnlyClient(), url)
}

// fetchInstallerWith is fetchInstaller with the client injected, so tests can
// supply an httptest TLS server's client (which trusts its throwaway cert)
// without loosening the scheme rules the real path enforces.
func fetchInstallerWith(client *http.Client, url string) (string, error) {
	if err := requireHTTPS(url); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch installer %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", &fetchStatusError{url: url, status: resp.Status, code: resp.StatusCode}
	}
	// Installers and bundles are a few KB; 1 MiB caps a misrouted response.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read installer %s: %w", url, err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return "", fmt.Errorf("fetch installer %s: empty response", url)
	}
	return string(body), nil
}

// fetchStatusError is a non-200 from the release-asset host, kept typed so
// runUpdate can tell "asset missing" apart from transport failures without
// string-matching the message.
type fetchStatusError struct {
	url    string
	status string
	code   int
}

func (e *fetchStatusError) Error() string {
	return fmt.Sprintf("fetch installer %s: unexpected status %s", e.url, e.status)
}

// missingAssetHint decorates a 404 from the release-asset fetch with its
// likely cause: the newest published release predates signed installer
// assets, which this build requires but that release cannot provide. Any
// other error passes through untouched.
func missingAssetHint(err error) error {
	var fse *fetchStatusError
	if errors.As(err, &fse) && fse.code == http.StatusNotFound {
		return fmt.Errorf("%w (the latest published release predates signed installer assets; install by hand with the documented one-liner, or wait for the next release)", err)
	}
	return err
}

// requireHTTPS rejects a URL that would fetch shell-bound content over an
// unauthenticated channel. Plain http means any router between here and the
// host can rewrite the script that is about to run as this user.
func requireHTTPS(raw string) error {
	u, err := neturl.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid installer URL %q: %w", raw, err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("refusing to fetch the installer over %q: %s is piped to a shell and must be https", u.Scheme, raw)
	}
	return nil
}

// httpsOnlyClient is http.DefaultClient with one difference: it refuses a
// redirect that leaves https. Go's default follows a downgrade silently, so
// without this the scheme check above only covers the first hop and a
// compromised or misconfigured host could bounce the fetch to plaintext.
func httpsOnlyClient() *http.Client {
	return &http.Client{CheckRedirect: httpsOnlyRedirect}
}

// httpsOnlyRedirect is the CheckRedirect policy httpsOnlyClient installs, kept
// separate so a test client can adopt the same rule.
func httpsOnlyRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return requireHTTPS(req.URL.String())
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
