package console

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
)

func TestServeNavigationJS(t *testing.T) {
	mux := newTestMux(t)
	rr := do(mux, httptest.NewRequest(http.MethodGet, "/console/static/navigation.js", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Header().Get("Content-Type"), "text/javascript")
	require.Contains(t, rr.Body.String(), "window.SeamConsole")
}

func TestLayout_LoadsSharedNavigationClient(t *testing.T) {
	_, mux := newConsole(t)
	page := getPeek(t, mux, "/console/")
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), `<script src="/console/static/navigation.js"></script>`)
}

// The sidebar is the one part of the morphed document whose SHAPE changes:
// toggling an optional feature adds or removes its nav entries. The client
// patches counts and the active marker in place (which is what preserves the
// count bump), and that patch is index-by-index -- so it silently does nothing
// when the two link lists have different lengths. These two tests pin the two
// halves of the fix together: the server really does render a different link
// set per feature state, and the client really does have the structural branch
// that copes with it. Without the branch the owner switches a feature off and
// the sidebar keeps offering its screens until the next full page load.
func TestNav_LinkSetChangesWithFeatureState(t *testing.T) {
	navLinks := regexp.MustCompile(`<nav class="nav".*?</nav>`)
	hrefs := regexp.MustCompile(`href="(/console/[^"]*)"`)
	linkSet := func(feats config.Features) []string {
		_, mux := newConsoleFeatures(t, feats)
		page := getPeek(t, mux, "/console/settings")
		require.Equal(t, http.StatusOK, page.Code)
		nav := navLinks.FindString(strings.ReplaceAll(page.Body.String(), "\n", " "))
		require.NotEmpty(t, nav, "the settings page must render the sidebar")
		var out []string
		for _, m := range hrefs.FindAllStringSubmatch(nav, -1) {
			out = append(out, m[1])
		}
		return out
	}

	off := linkSet(config.Features{})
	on := linkSet(config.Features{Research: true})

	require.NotEqual(t, len(off), len(on),
		"the two nav link lists must differ in LENGTH -- that is exactly the case the in-place count patch cannot express")
	for _, href := range []string{"/console/labs", "/console/trials"} {
		require.NotContains(t, off, href, "a disabled feature must not be offered in the sidebar")
		require.Contains(t, on, href, "an enabled feature must be offered in the sidebar")
	}
}

func TestNavigationJS_MorphsTheNavWhenItsLinkSetChanges(t *testing.T) {
	mux := newTestMux(t)
	rr := do(mux, httptest.NewRequest(http.MethodGet, "/console/static/navigation.js", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	js := rr.Body.String()

	require.Contains(t, js, "function navShape(",
		"the client must be able to tell that the sidebar's link set changed")
	require.Contains(t, js, "morphNode(currentNav, freshNav)",
		"a changed link set must morph the whole nav; the index-by-index count patch cannot add or remove entries")
	require.Regexp(t, `navShape\(currentNav\) !== navShape\(freshNav\)`, js,
		"the structural branch must be selected by comparing the two link sets")
}

func TestQueryForms_UseInPlaceNavigation(t *testing.T) {
	getForm := regexp.MustCompile(`<form[^>]*method="get"[^>]*>`)
	for _, name := range pageNames {
		source, err := templateFS.ReadFile("templates/" + name + ".html")
		require.NoError(t, err)
		for _, form := range getForm.FindAllString(string(source), -1) {
			require.Contains(t, form, "data-seam-query", "%s has a GET data form outside the shared no-reload path: %s", name, form)
		}
	}
}

func TestQueryControls_AreWiredAcrossConsole(t *testing.T) {
	wantMarkers := map[string]int{
		"overview":  1,
		"now":       1,
		"retrieval": 1,
		"projects":  4,
		"context":   2,
		"sessions":  5,
		"search":    5,
		"memories":  2,
		"notes":     2,
		"plans":     1,
		"trials":    2,
		"gardener":  2,
	}
	for name, want := range wantMarkers {
		source, err := templateFS.ReadFile("templates/" + name + ".html")
		require.NoError(t, err)
		require.GreaterOrEqual(t, strings.Count(string(source), "data-seam-query"), want,
			"%s must keep every filter, sort, search, and time-window control on the shared in-place path", name)
	}
}

func TestMutationForms_UseInPlaceNavigation(t *testing.T) {
	client := string(navigationJS)
	require.Contains(t, client, `method === 'post' && !!form.closest('.main')`,
		"owner POST forms inside the console view must use the shared no-reload path")
	require.Contains(t, client, `load(target.href, { method: 'POST'`)

	for _, name := range []string{"settings", "gardener"} {
		source, err := templateFS.ReadFile("templates/" + name + ".html")
		require.NoError(t, err)
		require.Contains(t, string(source), `document.addEventListener('seam:content-updated'`,
			"%s has page-owned controls that must be re-enhanced after a mutation patch", name)
	}
}

// The isolation control and its confirm step are pure server-rendered markup:
// the POST forms ride the shared mutation path (they live inside .main), the
// confirm step is a page state rather than a JS dialog, and the way out of it is
// a data-seam-query link that patches the view instead of navigating.
func TestIsolationControl_StaysOnTheSharedNoReloadPath(t *testing.T) {
	source, err := templateFS.ReadFile("templates/projectdetail.html")
	require.NoError(t, err)
	page := string(source)

	require.Contains(t, page, `<form class="iso-form" method="post" action="/console/projects/{{.Slug}}/isolation">`)
	require.Contains(t, page, `{{define "isolation-confirm"}}`)
	require.Contains(t, page, `<a class="btn small" href="{{.Cancel}}" data-seam-query>Cancel</a>`)
	require.Contains(t, page, `<a class="btn small" href="{{.Cancel}}" data-seam-query>Back</a>`)
	for _, banned := range []string{"confirm(", "alert(", "<dialog"} {
		require.NotContains(t, page, banned,
			"the tighten confirmation is server-rendered, not a JS dialog")
	}
}

func TestDataRefreshClients_NeverReloadDocument(t *testing.T) {
	layout, err := templateFS.ReadFile("templates/layout.html")
	require.NoError(t, err)

	for name, source := range map[string]string{
		"layout":     string(layout),
		"navigation": string(navigationJS),
		"library":    string(libraryJS),
	} {
		require.NotContains(t, source, "location.reload(", "%s must keep data refreshes inside the current document", name)
	}
	require.NotContains(t, string(libraryJS), "location.href = href",
		"a reader fetch failure must surface the error, not degrade into a document navigation")
	require.NotContains(t, string(libraryJS), "window.scrollTo(",
		"a reader swap must preserve the document position instead of looking like a reload")
	require.Contains(t, string(libraryJS), "reader=1",
		"a reader selection must request only the server-rendered reader fragment")
	require.Contains(t, string(libraryJS), "el.innerHTML = html",
		"a reader selection must render the fetched fragment into the existing reader")
	require.Contains(t, string(navigationJS), "morphNode(currentMain, freshMain)")
	require.Contains(t, string(navigationJS), "The current data is unchanged")
}
