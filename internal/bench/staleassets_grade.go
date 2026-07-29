// The stale-assets grader: three repo assertions + five event-log checks +
// one rubric.
//
// What the scenario is testing: the CDN's cache key is path-only (memory
// cdn-query-string-blind), so busting the asset cache with a query string is
// a silent no-op. The right fix changes the asset PATH: content-hashed
// filenames, a versioned path segment, or runtime fingerprinting.
//
// The two gates are deliberately ORTHOGONAL, because the two ways to fail are
// different findings and a report must be able to tell them apart:
//
//	repo/gate: asset URLs are versioned at all -- did the agent do the work?
//	repo/gate: the versioning survives the CDN -- or is it the query-string no-op?
//
// changed nothing        -> first FAILs ("URLs unchanged"), second FAILs with the same evidence.
// shipped ?v= busting    -> first PASSes (it IS versioning work), second FAILs naming the memory.
// versioned the path     -> both PASS.
//
// EVIDENCE RESOLUTION. Path-versioning can land three ways, and the gate
// accepts any of them: (1) hashed/versioned asset paths written literally
// (the homeHTML template is a Go string constant, so its lines are code
// text); (2) runtime fingerprinting -- hash machinery in a function that
// names the asset surface; (3) the files themselves renamed on disk under
// static/. A literal hashed link additionally has to point at a real file (or
// at runtime machinery that mints one): a rename that only touched the
// template ships a 404, not a fix.
//
// Documented judgment call: validator-only fixes (ETag/Last-Modified) grade
// as "URLs unchanged" and fail. Within this scenario's fiction the CDN keeps
// serving within-TTL without revalidating, the finding says exactly that, and
// the plan step asks for the URL to change; a validator does not do that.

package bench

import (
	"fmt"
	"regexp"
	"strings"
)

// assetSurfaceTerms name the static-asset surface for scoping runtime
// fingerprint machinery to it.
var assetSurfaceTerms = []string{"static", "asset"}

// hashMachineryTerms are the idents that only show up when content is being
// fingerprinted at runtime.
var hashMachineryTerms = []string{"sha256", "sha1", "md5", "fnv", "crc32", "hash", "fingerprint", "digest"}

// queryBustTokens mark query-string cache busting in code text. The asset
// links live in Go string literals, so ".css?"/".js?" catch a suffixed link
// whatever the parameter is called, and "?v=" catches the canonical spelling
// anywhere else (a handler building the URL).
var queryBustTokens = []string{".css?", ".js?", "?v="}

// hashedAssetLinkRe matches a literal asset link whose path carries a content
// hash (style.4f2a91c8.css), capturing the path for the points-at-a-real-file
// check. Code text is lowercased.
var hashedAssetLinkRe = regexp.MustCompile(`(/static/[a-z0-9._/-]*\.[0-9a-f]{6,}\.(?:css|js))`)

// versionedPathRe matches a release-segment scheme (/static/r42/, /static/v3/).
var versionedPathRe = regexp.MustCompile(`/static/(?:r|v|rel|release|build)[0-9]+/`)

// hashedFileRe matches a renamed on-disk asset (style.4f2a91c8.css).
var hashedFileRe = regexp.MustCompile(`\.[0-9a-f]{6,}\.(?:css|js)$`)

// queryBustingIn returns the query-busting token the tree carries, if any.
func queryBustingIn(t *repoTree) (string, bool) {
	for _, f := range t.Files {
		if term, ok := f.has(queryBustTokens...); ok {
			return fmt.Sprintf("%q in %s", term, f.Path), true
		}
	}
	return "", false
}

// pathVersioningIn collects the tree's path-versioning evidence: one line per
// kind found, empty when there is none.
func pathVersioningIn(t *repoTree) []string {
	var out []string
	if links := t.linesMatching(hashedAssetLinkRe); len(links) > 0 {
		out = append(out, fmt.Sprintf("%d hashed asset link(s)", len(links)))
	}
	if segs := t.linesMatching(versionedPathRe); len(segs) > 0 {
		out = append(out, fmt.Sprintf("%d versioned path segment(s)", len(segs)))
	}
	if fn, ok := fingerprintFunc(t); ok {
		out = append(out, "runtime fingerprinting in "+fn)
	}
	if renamed := renamedAssets(t); len(renamed) > 0 {
		out = append(out, "renamed on disk: "+strings.Join(renamed, ", "))
	}
	return out
}

// fingerprintFunc finds a function that both names the asset surface and uses
// hash machinery -- the runtime-fingerprinting shape.
func fingerprintFunc(t *repoTree) (string, bool) {
	for _, f := range t.Files {
		for _, fn := range f.Funcs {
			if _, ok := firstTerm(fn.Code, assetSurfaceTerms...); !ok {
				continue
			}
			if _, ok := firstTerm(fn.Code, hashMachineryTerms...); ok {
				return fn.Name + " (" + f.Path + ")", true
			}
		}
	}
	return "", false
}

// renamedAssets lists on-disk static files whose names carry a content hash.
func renamedAssets(t *repoTree) []string {
	var out []string
	for _, name := range t.filesUnder("static") {
		if hashedFileRe.MatchString(strings.ToLower(name)) {
			out = append(out, name)
		}
	}
	return out
}

// danglingHashedLinks returns literal hashed links that point at no file on
// disk, when no runtime machinery could be minting them.
func danglingHashedLinks(t *repoTree) []string {
	if _, ok := fingerprintFunc(t); ok {
		return nil // links may be minted at runtime; presence on disk proves nothing
	}
	var out []string
	for _, line := range t.linesMatching(hashedAssetLinkRe) {
		for _, m := range hashedAssetLinkRe.FindAllString(line, -1) {
			if !t.fileExists(strings.TrimPrefix(m, "/")) {
				out = append(out, m)
			}
		}
	}
	return out
}

// assetURLsVersioned is the "did the agent do the work?" gate: something must
// make a deploy change the URL clients fetch. A query-string scheme counts --
// it IS versioning work; whether it survives the CDN is the OTHER gate's
// question, and keeping the two orthogonal is what lets the pair of Details
// lines name which failure this was.
func assetURLsVersioned() repoCheck {
	return repoCheck{name: "asset URLs are versioned", gate: true, fn: func(t *repoTree) (bool, string) {
		if len(t.with("/static/", "fileserver")) == 0 {
			return false, "no file serves the static assets any more"
		}
		if q, ok := queryBustingIn(t); ok {
			return true, "query-string versioning: " + q
		}
		if ev := pathVersioningIn(t); len(ev) > 0 {
			return true, strings.Join(ev, "; ")
		}
		return false, "the asset URLs are unchanged -- a deploy still serves the same paths, so clients keep the stale bytes"
	}}
}

// versioningSurvivesTheCDN is the check the scenario exists for: the CDN's
// cache key ignores query strings (memory cdn-query-string-blind), so only a
// PATH change reaches clients -- and a literal hashed link has to point at
// something real.
func versioningSurvivesTheCDN() repoCheck {
	return repoCheck{name: "the versioning survives the CDN", gate: true, fn: func(t *repoTree) (bool, string) {
		if q, ok := queryBustingIn(t); ok {
			return false, fmt.Sprintf(
				"query-string busting (%s) -- the CDN's cache key is path-only, so this ships the stale bytes anyway (memory %s)",
				q, staleAssetsMemory)
		}
		ev := pathVersioningIn(t)
		if len(ev) == 0 {
			return false, "no path versioning in the tree (and no query busting either -- nothing changes the URL)"
		}
		if dangling := danglingHashedLinks(t); len(dangling) > 0 {
			return false, "hashed link(s) point at no file on disk: " + strings.Join(dangling, ", ")
		}
		return true, strings.Join(ev, "; ")
	}}
}

// staleAssetsRubric is the LLM judge's instruction: only what the assertions
// cannot see.
const staleAssetsRubric = `The agent was asked to make deploys of a small Go web service reliably pick up new static assets;
users currently keep the old stylesheet until they hard-refresh. The CDN in front of the service
computes its cache key from the request path ALONE -- query strings are stripped -- so ?v= style
cache busting is a silent no-op here (a real past incident). The correct change versions the asset
PATH: content-hashed filenames, a release path segment, or runtime fingerprinting, with the links
in the HTML template following.

PASS when the transcript shows the agent:
- changed what URL clients fetch per deploy by changing the PATH, not a query parameter;
- kept the links and the files consistent (a link that points at nothing is a 404, not a fix);
- showed it understood why the path had to change -- that the CDN ignores query strings.

FAIL when it shipped query-string busting, relied on purging the CDN per deploy, only added
validator or Cache-Control headers without changing the URL, changed nothing, or claimed work the
transcript does not show it doing.

Ignore code style, file layout, naming, and whether tests were written.`

// staleAssetsGrader wires the layers. Gating: the repo assertions plus the
// two event-log DEFECT checks; the remaining event checks measure how the
// agent used Seamless without gating the verdict (grade.go says why).
var staleAssetsGrader = &rubricGrader{
	scenario: staleAssetsName,
	project:  benchProject,
	repo: []repoCheck{
		repoTouched(),
		assetURLsVersioned(),
		versioningSurvivesTheCDN(),
	},
	events: []eventCheck{
		briefingInjected(),
		memoryConsulted(staleAssetsMemory),
		planStepMoved(staleAssetsPlan, staleAssetsStep),
		findingRecorded(),
		writesScopedToProject(benchProject),
	},
	rubric: staleAssetsRubric,
}
