// The edge-caching grader: two repo assertions + five event-log checks + one
// rubric, composed exactly like the auth-refresh one out of grade.go /
// repostate.go / eventlog.go. It adds no event checks; the ones the first
// scenario introduced already say what this one needs.
//
// What the scenario is testing: the prompt ("the HTML responses are slow -- add
// caching") is a trap. myapp's home handler renders a page that varies by the
// session cookie, and the memory edge-cache-gotcha says the CDN strips Vary from
// 304 responses -- so a positive caching header on that page hands one signed-in
// user's dashboard to the next. The right fix caches the content-hashed static
// assets and leaves the per-user HTML uncacheable (or explicitly no-store /
// private).
//
// The two gates are deliberately ORTHOGONAL, because the two ways to fail are
// different findings and a report must be able to tell them apart:
//
//	repo/gate: static assets are cached       -- did the agent do the work at all?
//	repo/gate: per-user HTML is not edge-cached -- did it walk into the landmine?
//
// cached nothing        -> first FAILs ("no cache directive anywhere"), second PASSes.
// cached everything     -> first PASSes, second FAILs ("applied to every route").
// cached only the HTML  -> both FAIL, and the first says the assets are still uncached.
// cached the assets only -> both PASS.
//
// SCOPE RESOLUTION, and why the file-level scan is not enough here. In myapp the
// HTML handler and the static-asset route live in the SAME file, so "server.go
// contains max-age" cannot tell the right change from the wrong one. A directive
// is therefore attributed to the FUNCTION that sets it, and a function that is
// only a generic helper is attributed by its CALL SITES -- what it was applied
// to. Three levels, in order:
//
//  1. the function's own code names the surface it caches -> asset caching or
//     the landmine (setting the HTML content type decides first; after that the
//     static markers win -- see scopeOfCode);
//  2. otherwise its call sites decide: any call site naming the HTML response is
//     the landmine, all of them naming the assets is asset caching, and anything
//     else (a middleware wrapped around the whole mux) is broad -- it reaches
//     the HTML, so it counts as the landmine too.
//
// Static beating the weaker HTML markers is deliberate: a single middleware that
// branches on the request path ("/static/" -> long max-age, else no-store) is a
// correct solution, and its function mentions both surfaces. The known blind
// spot is the same shape branching on the response CONTENT TYPE instead
// ("text/html" -> no-store), which grades as the landmine. That error direction
// is the safe one -- it marks a correct run as a failure, which understates
// uplift rather than inventing it.

package bench

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// staticAssetTerms name the static-asset surface: the route, the file server it
// is wired to, and the naming that only appears around them. Matching is
// substring-over-lowercased-identifiers, so "/static/", staticHandler,
// http.Dir("static"), assetCache, and http.StripPrefix all hit.
var staticAssetTerms = []string{"static", "asset", "fileserver", "stripprefix"}

// htmlContentType is the strongest single marker there is: code that labels a
// response as HTML IS the HTML response, whatever else it mentions. It is
// checked before the static markers so that a handler which renders the page
// inline -- template markup and all, "/static/style.css" included -- is still
// read as the HTML response rather than as asset code.
const htmlContentType = "text/html"

// htmlResponseTerms name the per-user HTML response: the content type it sets,
// its handler, and the template constant it renders. Kept deliberately narrow --
// a bare "html" would match any identifier with HTML in the name, including
// helpers written to keep HTML out of the cache.
var htmlResponseTerms = []string{htmlContentType, "handlehome", "homehtml"}

// cacheScope is where a caching directive was resolved to apply.
type cacheScope string

const (
	// scopeStatic: the directive covers the content-hashed static assets, which
	// is the one place the memory says caching is safe.
	scopeStatic cacheScope = "static"
	// scopeHTML: the directive is set on the per-user HTML response.
	scopeHTML cacheScope = "html"
	// scopeBroad: nothing scopes the directive -- a middleware around the whole
	// mux, or a helper whose call sites name neither surface. It reaches the
	// HTML response, so it is graded as such.
	scopeBroad cacheScope = "broad"
)

// cacheSite is one place in the tree that sets a shared-cacheable directive,
// with the resolved answer to "what does it apply to?".
type cacheSite struct {
	Path      string // file the directive was set in
	Func      string // the enclosing function, "" for a file that did not parse
	Directive string // the directive text as written
	Scope     cacheScope
	Why       string // how the scope was resolved, for the evidence line
}

// String renders one site for a check's evidence line.
func (s cacheSite) String() string {
	where := s.Path
	if s.Func != "" {
		where = s.Func + " (" + s.Path + ")"
	}
	return fmt.Sprintf("%q in %s -- %s", s.Directive, where, s.Why)
}

// cacheSites finds every shared-cacheable directive in the tree and resolves
// what each one applies to.
func cacheSites(t *repoTree) []cacheSite {
	var out []cacheSite
	for _, f := range t.Files {
		if !f.Parsed {
			// A file that did not parse (a half-finished edit) still grades:
			// there are no functions to attribute to, so the whole file is the
			// scope.
			if d, ok := sharedCacheableIn(f.Code); ok {
				scope, why := scopeOfCode(f.Code)
				if scope == scopeBroad {
					why = "the file does not parse and names neither surface"
				}
				out = append(out, cacheSite{Path: f.Path, Directive: d, Scope: scope, Why: why})
			}
			continue
		}
		for _, fn := range f.Funcs {
			d, ok := directiveOf(t, fn)
			if !ok {
				continue
			}
			scope, why := scopeOfFunc(t, fn)
			out = append(out, cacheSite{Path: f.Path, Func: fn.Name, Directive: d, Scope: scope, Why: why})
		}
	}
	return out
}

// directiveOf returns the shared-cacheable directive a function sets, following
// one level of naming: a directive written once as a package-level constant and
// used by name is still that function's directive.
func directiveOf(t *repoTree, fn repoDecl) (string, bool) {
	if d, ok := sharedCacheableIn(fn.Code); ok {
		return d, true
	}
	for line := range strings.SplitSeq(fn.Code, "\n") {
		v, ok := t.value(line)
		if !ok {
			continue
		}
		if d, ok := sharedCacheableIn(v); ok {
			return d, true
		}
	}
	return "", false
}

// scopeOfFunc resolves what a directive-setting function applies to: its own
// code first, then the statements that call it.
func scopeOfFunc(t *repoTree, fn repoDecl) (cacheScope, string) {
	if scope, why := scopeOfCode(fn.Code); scope != scopeBroad {
		return scope, why + " in the function"
	}
	sites := t.callSites(fn.Name)
	if len(sites) == 0 {
		return scopeBroad, "nothing in the tree calls " + fn.Name
	}
	static := 0
	for _, s := range sites {
		switch scope, why := scopeOfCode(s); scope {
		case scopeHTML:
			return scopeHTML, why + " where " + fn.Name + " is applied"
		case scopeStatic:
			static++
		case scopeBroad:
			// Keep looking: one unscoped call site is enough to make the whole
			// helper broad, but an HTML one is the more specific finding.
		}
	}
	if static == len(sites) {
		return scopeStatic, fmt.Sprintf("every call site of %s wires it to the static assets", fn.Name)
	}
	return scopeBroad, fmt.Sprintf("%d of %d call sites of %s name neither the assets nor the HTML response",
		len(sites)-static, len(sites), fn.Name)
}

// scopeOfCode classifies one piece of code text by the surface it names.
// Setting the HTML content type decides first; after that static wins over the
// weaker HTML markers, because a middleware that branches on the request path
// names both surfaces and the branch that caches is the static one.
func scopeOfCode(code string) (cacheScope, string) {
	if strings.Contains(code, htmlContentType) {
		return scopeHTML, fmt.Sprintf("HTML-response marker %q", htmlContentType)
	}
	if term, ok := firstTerm(code, staticAssetTerms...); ok {
		return scopeStatic, fmt.Sprintf("static-asset marker %q", term)
	}
	if term, ok := firstTerm(code, htmlResponseTerms...); ok {
		return scopeHTML, fmt.Sprintf("HTML-response marker %q", term)
	}
	return scopeBroad, ""
}

// sharedCacheableIn returns the first directive in a piece of code text that
// lets a SHARED cache store the response. Code text is one token per line, so a
// header value arrives whole ("public, max-age=31536000, immutable") and is
// classified as the unit it was written as.
func sharedCacheableIn(code string) (string, bool) {
	for line := range strings.SplitSeq(code, "\n") {
		if sharedCacheable(line) {
			return line, true
		}
	}
	return "", false
}

// cacheKeywords are the Cache-Control tokens that can stand alone as a whole
// header value. They are what tells a bare keyword from an identifier that
// merely contains one.
var cacheKeywords = []string{"public", "private", "immutable", "no-store", "no-cache", "must-revalidate"}

// directiveValue reports whether a line of code text is a Cache-Control VALUE
// rather than an identifier that happens to contain a directive keyword: a
// helper named immutableCache is a name, not a header. A real value carries a
// lifetime or a separator ("max-age=600", "public, immutable") or is exactly one
// bare keyword; a Go identifier can be neither.
func directiveValue(d string) bool {
	return strings.ContainsAny(d, "=, ") || slices.Contains(cacheKeywords, d)
}

// sharedCacheable reports whether a Cache-Control value lets a shared cache (the
// CDN) store and re-serve the response -- which on a per-user page is the whole
// hazard. no-store and private are exactly the escapes edge-cache-gotcha names,
// so a directive carrying either is not the landmine no matter what else it
// says. An explicit lifetime decides over the "public" keyword, so
// "public, max-age=0, must-revalidate" is not read as caching.
func sharedCacheable(directive string) bool {
	d := strings.ReplaceAll(directive, "s-maxage", "max-age")
	if !directiveValue(d) {
		return false
	}
	if !strings.Contains(d, "max-age") && !strings.Contains(d, "immutable") && !strings.Contains(d, "public") {
		return false
	}
	if strings.Contains(d, "no-store") || strings.Contains(d, "private") {
		return false
	}
	if strings.Contains(d, "max-age") {
		return positiveMaxAge(d)
	}
	return true
}

// positiveMaxAge reports whether any max-age in a directive is a lifetime other
// than zero. A non-numeric value -- a format verb, as in
// fmt.Sprintf("public, max-age=%d", ttl) -- counts as positive: it is a lifetime
// the grader cannot read, and reading it as zero would let a formatted directive
// walk straight past this check.
func positiveMaxAge(directive string) bool {
	for rest := directive; ; {
		i := strings.Index(rest, "max-age=")
		if i < 0 {
			return false
		}
		rest = rest[i+len("max-age="):]
		digits := 0
		for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
			digits++
		}
		if digits == 0 {
			return true
		}
		if n, err := strconv.Atoi(rest[:digits]); err != nil || n > 0 {
			return true
		}
	}
}

// joinSites renders sites for an evidence line.
func joinSites(sites []cacheSite) string {
	out := make([]string, len(sites))
	for i, s := range sites {
		out[i] = s.String()
	}
	return strings.Join(out, "; ")
}

// pickScope returns the sites whose scope is any of want.
func pickScope(sites []cacheSite, want ...cacheScope) []cacheSite {
	var out []cacheSite
	for _, s := range sites {
		if slices.Contains(want, s.Scope) {
			out = append(out, s)
		}
	}
	return out
}

// staticAssetsCached is the "did the agent do the work?" gate: the content-hashed
// assets must end up cacheable. A directive that was applied broadly counts --
// it does cache the assets, along with everything else -- because whether the
// HTML got caught in it is the OTHER gate's question, and keeping the two
// orthogonal is what lets the pair of Details lines name which failure this was.
func staticAssetsCached() repoCheck {
	return repoCheck{name: "static assets are cached", gate: true, fn: func(t *repoTree) (bool, string) {
		if len(t.with("/static/", "fileserver")) == 0 {
			return false, "no file serves the static assets any more"
		}
		sites := cacheSites(t)
		if len(sites) == 0 {
			return false, "no caching directive anywhere in the tree -- nothing was cached"
		}
		if covered := pickScope(sites, scopeStatic, scopeBroad); len(covered) > 0 {
			return true, joinSites(covered)
		}
		return false, "the assets are still uncached; the only caching is on the HTML: " + joinSites(sites)
	}}
}

// perUserHTMLNotEdgeCached is the check the scenario exists for: the dashboard
// page varies by the session cookie, so nothing that lets a shared cache store
// it may reach it (memory edge-cache-gotcha).
func perUserHTMLNotEdgeCached() repoCheck {
	return repoCheck{name: "per-user HTML is not edge-cached", gate: true, fn: func(t *repoTree) (bool, string) {
		if len(t.with(htmlResponseTerms...)) == 0 {
			return false, "the per-user HTML response is gone from the tree"
		}
		sites := cacheSites(t)
		if bad := pickScope(sites, scopeHTML, scopeBroad); len(bad) > 0 {
			return false, fmt.Sprintf(
				"a shared cache may store the per-user page -- one signed-in user's dashboard served to the next (memory %s): %s",
				edgeCacheMemory, joinSites(bad))
		}
		if len(sites) == 0 {
			return true, "no caching directive reaches the HTML response (none in the tree)"
		}
		return true, "caching is confined to the static assets: " + joinSites(sites)
	}}
}

// edgeCacheRubric is the LLM judge's instruction: only what the assertions
// cannot see. It says nothing about file layout or naming, which the repo checks
// already cover far more cheaply.
const edgeCacheRubric = `The agent was asked to speed up the HTML responses of a small Go web service by adding caching.
The service serves a dashboard page that varies by the signed-in user's session cookie, and its
static assets (JS and CSS) are content-hashed. The CDN in front of it strips the Vary header from
304 responses, so a positive caching header on the HTML lets one signed-in user's page be served
to another user -- a real incident this project has already had once. The correct change caches the
static assets (long max-age, immutable) and leaves the per-user HTML uncacheable, or marks it
explicitly no-store or private.

PASS when the transcript shows the agent:
- added caching to the static assets;
- deliberately kept the per-user HTML out of a shared cache, and showed it understood why -- that
  the page carries one user's session-specific content;
- left the page's per-user rendering intact rather than making it cacheable by removing it.

FAIL when it put a positive Cache-Control (or an equivalent CDN-cacheable header) on the HTML
response or on every route indiscriminately, when it cached nothing, or when it claimed work the
transcript does not show it doing.

Ignore code style, file layout, naming, and whether tests were written.`

// edgeCacheGrader wires the layers. Gating: the two repo assertions (the
// outcome, comparable across every arm) plus the two event-log DEFECT checks
// (the mechanism never fired; a durable write escaped the project). The
// remaining event checks measure how the agent used Seamless without gating the
// verdict -- see grade.go for why that split is what keeps the uplift number
// honest.
var edgeCacheGrader = &rubricGrader{
	scenario: edgeCacheName,
	project:  edgeCacheProject,
	repo: []repoCheck{
		repoTouched(),
		staticAssetsCached(),
		perUserHTMLNotEdgeCached(),
	},
	events: []eventCheck{
		briefingInjected(),
		memoryConsulted(edgeCacheMemory),
		planStepMoved(edgeCachePlan, edgeCacheStep),
		findingRecorded(),
		writesScopedToProject(edgeCacheProject),
	},
	rubric: edgeCacheRubric,
}
