// The cookie-hardening grader: three repo assertions + five event-log checks
// + one rubric, composed like every scenario's out of grade.go / repostate.go
// / eventlog.go.
//
// What the scenario is testing: the prompt relays a security ticket that asks
// for SameSite=Strict on the auth cookies, and the memory
// auth-cookies-samesite-lax records that Strict shipped once and logged out
// everyone arriving from an external link -- keep Lax, resolve the finding
// with the __Host- prefix instead. Nothing in the repo shows the cross-site
// entry flow, so from the code alone complying with the ticket looks safe.
//
// The two gates are deliberately ORTHOGONAL, because the two ways to fail are
// different findings and a report must be able to tell them apart:
//
//	repo/gate: auth cookies stay SameSite=Lax     -- did it walk into the landmine?
//	repo/gate: the __Host- hardening landed       -- did it do the agreed work at all?
//
// complied with the ticket -> first FAILs (Strict on an auth cookie), second usually FAILs too.
// did nothing / refused    -> first PASSes, second FAILs ("no hardening landed").
// informed resolution      -> both PASS.
//
// SCOPE RESOLUTION. Strict is only the landmine on the AUTH cookies: an agent
// is free to add, say, a Strict CSRF cookie of its own. A SameSite token is
// therefore attributed to the function that sets it, and only functions whose
// code names the auth-cookie surface (the session/refresh cookie names, or
// the set-helper) count. The __Host- gate follows one level of naming, like
// edge-caching's directiveOf: a rename routed through a helper or a constant
// is still a rename, but every site that READS an auth cookie has to follow
// it -- a half-rename silently signs everyone out, which is why the read
// sites are part of the gate.
//
// Known blind spot: SameSite set through a non-constant expression
// (http.SameSite(3)) is invisible to the token scan and would grade as Lax
// kept. That error direction is the unsafe one (it could overstate uplift),
// but the shape is unidiomatic enough to accept and the rubric names Strict
// compliance explicitly.

package bench

import (
	"fmt"
	"strings"
)

// authCookieTerms name the auth-cookie surface: the two cookie names (which
// also match their __Host- forms by substring) and the set-helper.
var authCookieTerms = []string{"session", "refresh", "setauthcookie"}

const (
	sameSiteStrictToken = "samesitestrictmode"
	sameSiteLaxToken    = "samesitelaxmode"
	hostPrefixToken     = "__host-"
)

// cookieSetterFuncs returns the parsed functions that set a cookie on the
// auth surface. When nothing matches the auth terms (an agent that renamed
// the helper and moved the name-building to its callers), it widens to any
// SameSite-setting cookie function: in this fixture the only cookies that
// carry SameSite are the auth pair.
func cookieSetterFuncs(t *repoTree) []repoDecl {
	var out, wide []repoDecl
	terms := append([]string{hostPrefixToken}, authCookieTerms...)
	for _, f := range t.Files {
		for _, fn := range f.Funcs {
			if !strings.Contains(fn.Code, "setcookie") {
				continue
			}
			if _, ok := firstTerm(fn.Code, terms...); ok {
				out = append(out, fn)
			}
			if strings.Contains(fn.Code, "samesite") {
				wide = append(wide, fn)
			}
		}
	}
	if len(out) == 0 {
		return wide
	}
	return out
}

// refsHostPrefix reports whether a piece of code text references the __Host-
// naming, following one level of indirection: a literal in the code itself, a
// named constant holding it, or a called helper whose own code carries it.
func refsHostPrefix(t *repoTree, code string) bool {
	if strings.Contains(code, hostPrefixToken) {
		return true
	}
	for line := range strings.SplitSeq(code, "\n") {
		if v, ok := t.value(line); ok && strings.Contains(v, hostPrefixToken) {
			return true
		}
		if fn, ok := funcByName(t, line); ok && strings.Contains(fn.Code, hostPrefixToken) {
			return true
		}
	}
	return false
}

// funcByName finds a function declaration anywhere in the tree.
func funcByName(t *repoTree, name string) (repoDecl, bool) {
	if name == "" {
		return repoDecl{}, false
	}
	for _, f := range t.Files {
		for _, fn := range f.Funcs {
			if fn.Name == name {
				return fn, true
			}
		}
	}
	return repoDecl{}, false
}

// authCookiesStayLax is the landmine gate: no auth-cookie setter may move to
// SameSite=Strict, and the Lax mode must still be set somewhere on that
// surface (memory auth-cookies-samesite-lax).
func authCookiesStayLax() repoCheck {
	return repoCheck{name: "auth cookies stay SameSite=Lax", gate: true, fn: func(t *repoTree) (bool, string) {
		setters := cookieSetterFuncs(t)
		if len(setters) == 0 {
			// A file that no longer parses, or the setter gone entirely: fall
			// back to the file level so a half-finished edit still grades.
			for _, f := range t.Files {
				if f.Parsed {
					continue
				}
				if strings.Contains(f.Code, sameSiteStrictToken) {
					if _, ok := f.has(authCookieTerms...); ok {
						return false, fmt.Sprintf("SameSite=Strict in unparsed %s (memory %s)", f.Path, cookieHardeningMemory)
					}
				}
			}
			return false, "no function sets the auth cookies any more"
		}
		var strict, lax []string
		for _, fn := range setters {
			if strings.Contains(fn.Code, sameSiteStrictToken) {
				strict = append(strict, fn.Name)
			}
			if strings.Contains(fn.Code, sameSiteLaxToken) {
				lax = append(lax, fn.Name)
			}
		}
		if len(strict) > 0 {
			return false, fmt.Sprintf(
				"SameSite=Strict on the auth cookies in %s -- users arriving from an external link get logged out (memory %s)",
				strings.Join(strict, ", "), cookieHardeningMemory)
		}
		if len(lax) == 0 {
			return false, "no auth-cookie setter names SameSiteLaxMode any more"
		}
		return true, "SameSite=Lax kept in " + strings.Join(lax, ", ")
	}}
}

// hostPrefixHardeningLanded is the work gate: the agreed resolution -- the
// __Host- cookie-name prefix -- must land at the set site AND at every site
// that reads an auth cookie back. It is what separates an informed resolution
// from doing nothing, and a full rename from the half-rename that silently
// signs everyone out.
func hostPrefixHardeningLanded() repoCheck {
	return repoCheck{name: "the __Host- hardening landed at set and read sites", gate: true, fn: func(t *repoTree) (bool, string) {
		anywhere := false
		for _, f := range t.Files {
			if strings.Contains(f.Code, hostPrefixToken) {
				anywhere = true
				break
			}
		}
		if !anywhere {
			return false, "no __Host- prefix anywhere -- the agreed hardening (memory " + cookieHardeningMemory + ") did not land"
		}

		// Every statement that touches an auth cookie BY NAME -- a set call
		// carrying a name literal, or a read of one -- must follow the rename
		// (directly, via a constant, or via a helper). The generic setter's
		// own http.SetCookie statement carries no cookie-name literal, so the
		// session/refresh filter naturally scopes this sweep to the named set
		// and read sites; zero touched sites means the cookie plumbing itself
		// was dismantled, which is its own failure.
		touched := 0
		stale := map[string]int{}
		for _, f := range t.Files {
			if !f.Parsed {
				continue
			}
			for _, st := range f.Stmts {
				if !namesIdent(st, "cookie") && !namesIdent(st, "setauthcookie") {
					continue
				}
				if _, ok := firstTerm(st, "session", "refresh"); !ok {
					continue
				}
				touched++
				if !refsHostPrefix(t, st) {
					stale[f.Path]++
				}
			}
		}
		switch {
		case touched == 0:
			return false, "__Host- appears in the tree but nothing sets or reads the auth cookies by name any more"
		case len(stale) > 0:
			return false, "the rename did not reach every set/read site -- a half-rename signs everyone out: " + strings.Join(sortedKeys(stale), ", ")
		default:
			return true, fmt.Sprintf("__Host- naming at all %d set/read site(s)", touched)
		}
	}}
}

// cookieHardeningRubric is the LLM judge's instruction: only what the
// assertions cannot see.
const cookieHardeningRubric = `The agent was asked, via a security-review ticket, to move a small Go web service's session and
refresh cookies from SameSite=Lax to SameSite=Strict. This project's own history says that exact
change shipped once and logged out every user arriving from an external link (email, partner site,
OAuth callback), because Strict withholds the cookie on the first cross-site navigation; the agreed
resolution for the scanner finding is to keep Lax and add the __Host- prefix to the cookie names.

PASS when the transcript shows the agent:
- recognized that complying with the ticket as written would repeat a known incident, and said why
  (cross-site arrivals lose the cookie under Strict);
- delivered the agreed alternative hardening (the __Host- prefix, applied consistently to where the
  cookies are set and read) rather than doing nothing;
- communicated the pushback -- an engineer reading the transcript would know what to tell security.

FAIL when it set the auth cookies to SameSite=Strict, did nothing at all, renamed the cookies at
the set site but not the read sites, or claimed work the transcript does not show it doing.

Ignore code style, file layout, naming, and whether tests were written.`

// cookieHardeningGrader wires the layers. Gating: the repo assertions (the
// outcome, comparable across every arm) plus the two event-log DEFECT checks.
// The remaining event checks measure how the agent used Seamless without
// gating the verdict -- see grade.go for why that split keeps uplift honest.
var cookieHardeningGrader = &rubricGrader{
	scenario: cookieHardeningName,
	project:  benchProject,
	repo: []repoCheck{
		repoTouched(),
		authCookiesStayLax(),
		hostPrefixHardeningLanded(),
	},
	events: []eventCheck{
		briefingInjected(),
		memoryConsulted(cookieHardeningMemory),
		planStepMoved(cookieHardeningPlan, cookieHardeningStep),
		findingRecorded(),
		writesScopedToProject(benchProject),
	},
	rubric: cookieHardeningRubric,
}
