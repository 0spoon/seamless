package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/arctop/seamless/internal/config"
	"github.com/arctop/seamless/internal/store"
	"github.com/arctop/seamless/internal/update"
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
	// releaseDownloadBase resolves to GitHub's "latest" release's assets (the
	// repository is update.Repo, which the installer scripts hardcode too).
	releaseDownloadBase = "https://github.com/" + update.Repo + "/releases/latest/download"
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
	check := fs.Bool("check", false, "report installed vs the newest release, and what the update check is doing, then exit without changing anything")
	dryRun := fs.Bool("dry-run", false, "print what would run and exit without fetching or executing")
	urlFlag := fs.String("url", "", "override the installer URL (default: the canonical thereisnospoon.org installer for this OS)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *check {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		return reportUpdateCheck(ctx, os.Stdout, update.NewFetcher(), runtime.GOOS, version, buildVersion(), cliUpdateView(ctx))
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
	if err := update.RequireHTTPS(url); err != nil {
		return "", fmt.Errorf("the installer is piped to a shell: %w", err)
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

// httpsOnlyClient is http.DefaultClient with one difference: it refuses a
// redirect that leaves https (update.HTTPSOnlyRedirect). Go's default follows a
// downgrade silently, so without this the scheme check in fetchInstallerWith
// only covers the first hop and a compromised or misconfigured host could
// bounce the fetch to plaintext.
func httpsOnlyClient() *http.Client {
	return &http.Client{CheckRedirect: update.HTTPSOnlyRedirect}
}

// cliUpdateView is the update view `update --check` adds its mode and
// last-check rows from, or nil when no config loads (the check itself still
// runs). The database is opened read-only and only to read the console's
// override: a newer CLI must never migrate a database an older daemon serves.
func cliUpdateView(ctx context.Context) *updateView {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	var db *sql.DB
	if !cfg.IsClient() {
		if d, oerr := store.OpenExisting(cfg.DBPath()); oerr == nil {
			db = d
			defer func() { _ = db.Close() }()
		}
	}
	v := loadUpdateView(ctx, cfg, db)
	return &v
}

// reportUpdateCheck reads the release list and compares its newest
// installable release for goos with the running build, printing a short
// verdict. current is the bare version that is compared; display is what the
// "current" row shows (it carries +commit). view, when set, adds what the
// daemon's update check is doing (mode, last check). It changes nothing, and
// it asks regardless of update.check: running it is the owner asking.
//
// "Newest" is the highest version the list holds once drafts, prereleases,
// irregular tags and releases still uploading their assets are dropped
// (update.Filter) -- not GitHub's "latest", which a backport can make an older
// version (memory github-releases-latest-is-not-max-version).
func reportUpdateCheck(ctx context.Context, w io.Writer, f *update.Fetcher, goos, current, display string, view *updateView) error {
	page, err := f.Fetch(ctx, "")
	if err != nil {
		if errors.Is(err, update.ErrRateLimited) {
			return fmt.Errorf("seamlessd.update: %w (try again later, or pin a version: SEAMLESS_VERSION=x.y.z seamlessd update)", err)
		}
		return fmt.Errorf("seamlessd.update: %w", err)
	}

	fmt.Fprintf(w, "\n%s %s\n", bold("Seamless"), dim("update --check"))
	fieldRowTo(w, "current", display)
	newest, ok := update.Newest(update.Filter(page.Releases, goos))
	if !ok {
		fieldRowTo(w, "newest", yellow("none")+dim(" -- no installable release for "+goos+" in the newest 20"))
		return nil
	}
	fieldRowTo(w, "newest", "v"+newest.Version.String()+dim(" (published "+newest.PublishedAt.UTC().Format("2006-01-02")+")"))

	cur, curOK := update.Parse(current)
	switch {
	case !curOK:
		fieldRowTo(w, "status", yellow("development build")+dim(" -- 'seamlessd update' installs the newest release"))
	case cur.Compare(newest.Version) < 0:
		fieldRowTo(w, "status", green("update available")+dim(" -- run 'seamlessd update' to upgrade"))
	case cur.Compare(newest.Version) == 0:
		fieldRowTo(w, "status", dim("up to date"))
	default:
		fieldRowTo(w, "status", dim("ahead of the newest published release"))
	}
	if view != nil {
		updateCheckRows(w, *view, time.Now())
	}
	return nil
}
