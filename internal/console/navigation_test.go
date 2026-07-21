package console

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
		"retrieval": 1,
		"projects":  4,
		"sessions":  4,
		"search":    5,
		"memories":  2,
		"notes":     2,
		"plans":     1,
		"trials":    2,
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
	require.Contains(t, string(navigationJS), "morphNode(currentMain, freshMain)")
	require.Contains(t, string(navigationJS), "The current data is unchanged")
}
