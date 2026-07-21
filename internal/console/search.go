package console

// Global search: one query across every entity the console can link to.
//
// Two surfaces share this handler. The page (GET /console/search) runs the full
// fused semantic+FTS retrieval; the command palette fetches the same route with
// ?format=json&fast=1, which drops the semantic leg so a query per keystroke
// never costs a remote embedding round-trip. The JSON shape is the palette's
// contract and is CLI-visible, so its field names are stable surface.
//
// Coverage is deliberately partial. Memories and notes come through
// retrieve.Search (fused, snippeted); tasks, plans, trials, projects, and
// sessions have no FTS mirror and match by LIKE (store.Search*). Events are
// excluded because the telemetry stream has its own Interactions surface and
// would flood results.

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

// searchScopes are the accepted ?scope values: "all", the two knowledge kinds
// retrieve.Search understands, and one per structured entity.
var searchScopes = []string{"all", "memories", "notes", "tasks", "plans", "trials", "projects", "sessions"}

// searchWindowKeys / searchSortKeys are the strict URL enums for the search
// controls. Search defaults to all time so adding the window control does not
// silently hide results that the page exposed before it existed.
var (
	searchWindowKeys = []string{"24h", "7d", "30d", "1y", "all"}
	searchSortKeys   = []string{"relevance", "newest", "oldest", "project", "confidence"}
)

// searchQueryMax caps the query length. Longer input is truncated rather than
// rejected: a paste is a clumsy search, not an error.
const searchQueryMax = 200

// searchQueryMin is the shortest query worth running. It matches the floor in
// store's ftsQuery, which drops single-character tokens -- below this the
// lexical leg has nothing to match and the page would promise results it cannot
// deliver.
const searchQueryMin = 2

// Result depth per group: the page shows a full page of hits, the palette shows
// a preview it can render in a dropdown.
const (
	searchPageLimit = 20
	searchFastLimit = 5
)

// searchRow is one result, already resolved to the URL it links to. Href is
// always an in-console path; Peek marks the rows the detail pane can load as a
// fragment (every kind here can, but the flag keeps the template honest).
type searchRow struct {
	Kind        string        `json:"kind"`
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Project     string        `json:"project,omitempty"`
	Age         string        `json:"age"`
	Href        string        `json:"href"`
	Description string        `json:"description,omitempty"`
	SnippetHTML template.HTML `json:"snippetHtml,omitempty"`
	Updated     time.Time     `json:"updated"`
	Lexical     bool          `json:"lexical,omitempty"`
	// Similarity is the semantic leg's cosine similarity as a percentage
	// (1-100), so the observer can see where relevance falls off. Zero for a
	// lexical-only hit -- an FTS match has a highlighted snippet instead of a
	// distance -- and for the structured-entity groups, which match by LIKE.
	Similarity int    `json:"similarity,omitempty"`
	ScoreTone  string `json:"-"`
	Favorite   bool   `json:"favorite,omitempty"`
	Peek       bool   `json:"peek"`
}

// searchGroup is one entity kind's results.
type searchGroup struct {
	Kind  string      `json:"kind"`
	Label string      `json:"label"`
	Count int         `json:"count"`
	Rows  []searchRow `json:"rows"`
}

// searchData is the page/JSON payload.
type searchData struct {
	Query       string               `json:"query"`
	Scope       string               `json:"scope"`
	Sort        string               `json:"sort"`
	Window      string               `json:"window"`
	WindowLabel string               `json:"windowLabel"`
	Fast        bool                 `json:"fast"`
	Fav         bool                 `json:"favoritesOnly"`
	Groups      []searchGroup        `json:"groups"`
	Total       int                  `json:"total"`
	Rows        []searchRow          `json:"-"` // unified, globally sorted page projection
	Since       time.Time            `json:"-"`
	Scopes      []searchScope        `json:"-"`
	Windows     []searchWindowOption `json:"-"`
	Sorts       []searchSortOption   `json:"-"`
}

// highlightSnippet renders an FTS snippet as safe HTML with the matched terms
// wrapped in <mark>.
//
// The order below is the whole security argument and must not be reversed:
// escape the raw item text FIRST, so any markup a writer put in their own body
// becomes inert entities, and only THEN substitute the sentinels for real tags.
// Escaping second would escape our own <mark>; substituting first would let an
// item's body smuggle live markup through. The sentinels are control characters
// (store.SnippetStartMark/EndMark) precisely so this substitution cannot collide
// with anything HTMLEscapeString emits -- an attacker who embeds a literal
// sentinel in their body gets a stray, inert <mark>, never an injection.
func highlightSnippet(raw string) template.HTML {
	esc := template.HTMLEscapeString(raw)
	esc = strings.ReplaceAll(esc, store.SnippetStartMark, "<mark>")
	esc = strings.ReplaceAll(esc, store.SnippetEndMark, "</mark>")
	return template.HTML(esc)
}

// wants reports whether scope selects the given kind.
func (d searchData) wants(kind string) bool { return d.Scope == "all" || d.Scope == kind }

var searchParamNames = []string{"q", "scope", "w", "sort", "fast", "fav", "format"}

// validateSearchParams rejects misspelled and ambiguous query parameters. The
// search controls are bookmarkable API inputs: silently ignoring ?srot=project
// would render a plausible relevance-ordered page that the caller cannot tell
// from success.
func validateSearchParams(values url.Values) error {
	for key, vals := range values {
		if !slices.Contains(searchParamNames, key) {
			return fmt.Errorf("invalid parameter %q: valid parameters are %s", key, strings.Join(searchParamNames, ", "))
		}
		if len(vals) != 1 {
			return fmt.Errorf("parameter %q must be provided exactly once", key)
		}
	}
	if format, ok := values["format"]; ok && format[0] != "json" {
		return fmt.Errorf("invalid format %q: valid value is json (or omit)", format[0])
	}
	return nil
}

// searchEnumParam applies a default only when the key is absent. An explicitly
// empty or unknown value is an error, matching the console's no-silent-fallback
// boundary contract.
func searchEnumParam(values url.Values, key, fallback string, valid []string) (string, error) {
	vals, ok := values[key]
	if !ok {
		return fallback, nil
	}
	value := vals[0]
	if !slices.Contains(valid, value) {
		return "", fmt.Errorf("invalid %s %q: valid values are %s", key, value, strings.Join(valid, ", "))
	}
	return value, nil
}

func (s *Service) searchPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	values := r.URL.Query()
	if err := validateSearchParams(values); err != nil {
		s.badRequest(w, r, err.Error())
		return
	}
	q := strings.TrimSpace(values.Get("q"))
	if n := []rune(q); len(n) > searchQueryMax {
		q = strings.TrimSpace(string(n[:searchQueryMax]))
	}
	scope, err := searchEnumParam(values, "scope", "all", searchScopes)
	if err != nil {
		s.badRequest(w, r, err.Error())
		return
	}
	sortKey, err := searchEnumParam(values, "sort", "relevance", searchSortKeys)
	if err != nil {
		s.badRequest(w, r, err.Error())
		return
	}
	windowKey, err := searchEnumParam(values, "w", "all", searchWindowKeys)
	if err != nil {
		s.badRequest(w, r, err.Error())
		return
	}
	fastParam := values.Get("fast")
	if _, present := values["fast"]; present && fastParam != "1" {
		s.badRequest(w, r, fmt.Sprintf("invalid fast %q: valid values are 1 (or omit)", fastParam))
		return
	}
	fast := fastParam == "1"
	favParam := values.Get("fav")
	if _, present := values["fav"]; present && favParam != "1" {
		s.badRequest(w, r, fmt.Sprintf("invalid fav %q: valid values are 1 (or omit)", favParam))
		return
	}
	fav := favParam == "1"
	window := resolveSearchWindow(windowKey, time.Now().UTC())

	data := searchData{
		Query: q, Scope: scope, Sort: sortKey,
		Window: window.Key, WindowLabel: window.Label, Since: window.Since,
		Fast: fast, Fav: fav, Scopes: searchScopeOptions(scope),
		Windows: searchWindowOptions(window.Key), Sorts: searchSortOptions(sortKey),
	}
	if len([]rune(q)) < searchQueryMin {
		// Too short to match anything: render the empty state rather than a
		// query that would promise results it cannot find.
		s.render(w, r, "search", pageData{Title: "Search", Active: "search", Data: data})
		return
	}

	limit := searchPageLimit
	if fast {
		limit = searchFastLimit
	}
	// The queries run sequentially on purpose: the pool is SetMaxOpenConns(1),
	// so fanning out over goroutines would serialize on the connection anyway
	// while adding failure modes.
	groups, err := s.searchGroups(ctx, data, limit)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if data.Fav {
		groups = filterFavoriteGroups(groups)
	}
	data.Groups = groups
	for _, g := range groups {
		data.Total += g.Count
		data.Rows = append(data.Rows, g.Rows...)
	}
	sortSearchRows(data.Rows, data.Sort)
	s.render(w, r, "search", pageData{Title: "Search", Active: "search", Data: data})
}

// searchGroups runs each in-scope entity query and returns the non-empty groups
// in display order.
func (s *Service) searchGroups(ctx context.Context, data searchData, limit int) ([]searchGroup, error) {
	var groups []searchGroup
	add := func(kind, label string, rows []searchRow) {
		if len(rows) > 0 {
			groups = append(groups, searchGroup{Kind: kind, Label: label, Count: len(rows), Rows: rows})
		}
	}

	// Memories + notes: one fused retrieval covering both, split into groups
	// after the fact so a single ranking decides what surfaces.
	if s.cfg.Retrieve != nil && (data.wants("memories") || data.wants("notes")) {
		scope := "all"
		if data.Scope == "memories" || data.Scope == "notes" {
			scope = data.Scope
		}
		hits, err := s.cfg.Retrieve.Search(ctx, retrieve.SearchInput{
			Query: data.Query, Scope: scope, Limit: limit, Semantic: !data.Fast, Since: data.Since,
		})
		if err != nil {
			return nil, err
		}
		var mems, notes []searchRow
		for _, h := range hits {
			row := hitRow(h)
			if h.Kind == "note" {
				notes = append(notes, row)
			} else {
				mems = append(mems, row)
			}
		}
		add("memories", "Memories", mems)
		add("notes", "Notes", notes)
	}

	if data.wants("tasks") {
		tasks, err := store.SearchTasksSince(ctx, s.cfg.DB, data.Query, data.Since, limit)
		if err != nil {
			return nil, err
		}
		rows := make([]searchRow, 0, len(tasks))
		for _, t := range tasks {
			rows = append(rows, searchRow{
				Kind: "task", ID: t.ID, Title: t.Title, Project: t.ProjectSlug,
				Age: ago(t.UpdatedAt), Href: "/console/tasks/" + t.ID,
				Description: string(t.Status), Updated: t.UpdatedAt,
				Favorite: t.Favorite, Lexical: true, Peek: true,
			})
		}
		add("tasks", "Tasks", rows)
	}

	if data.wants("plans") {
		plans, err := store.SearchPlansSince(ctx, s.cfg.DB, data.Query, data.Since, limit)
		if err != nil {
			return nil, err
		}
		rows := make([]searchRow, 0, len(plans))
		for _, p := range plans {
			rows = append(rows, searchRow{
				Kind: "plan", ID: p.Slug, Title: p.Title, Project: p.Project,
				Age: ago(p.Updated), Updated: p.Updated, Href: "/console/plans/" + p.Slug,
				Favorite: p.Favorite, Lexical: true, Peek: true,
			})
		}
		add("plans", "Plans", rows)
	}

	if data.wants("trials") {
		trials, err := store.SearchTrialsSince(ctx, s.cfg.DB, data.Query, data.Since, limit)
		if err != nil {
			return nil, err
		}
		rows := make([]searchRow, 0, len(trials))
		for _, tr := range trials {
			desc := "lab " + tr.Lab
			if tr.Outcome != "" {
				desc += " · " + string(tr.Outcome)
			}
			rows = append(rows, searchRow{
				Kind: "trial", ID: tr.ID, Title: tr.Title, Project: tr.ProjectSlug,
				Age: ago(tr.CreatedAt), Href: "/console/trials/" + tr.ID,
				Description: desc, Updated: tr.CreatedAt,
				Favorite: tr.Favorite, Lexical: true, Peek: true,
			})
		}
		add("trials", "Trials", rows)
	}

	if data.wants("projects") {
		projects, err := store.SearchProjectsSince(ctx, s.cfg.DB, data.Query, data.Since, limit)
		if err != nil {
			return nil, err
		}
		rows := make([]searchRow, 0, len(projects))
		for _, p := range projects {
			rows = append(rows, searchRow{
				Kind: "project", ID: p.Slug, Title: p.Slug, Project: p.Slug, Age: ago(p.UpdatedAt),
				Href: "/console/projects/" + p.Slug, Description: p.Name,
				Updated: p.UpdatedAt, Favorite: p.Favorite, Lexical: true, Peek: true,
			})
		}
		add("projects", "Projects", rows)
	}

	if data.wants("sessions") {
		sessions, err := store.SearchSessionsSince(ctx, s.cfg.DB, data.Query, data.Since, limit)
		if err != nil {
			return nil, err
		}
		rows := make([]searchRow, 0, len(sessions))
		for _, sess := range sessions {
			rows = append(rows, searchRow{
				Kind: "session", ID: sess.ID, Title: sess.Name, Project: sess.ProjectSlug,
				Age: ago(sess.UpdatedAt), Href: "/console/sessions/" + sess.ID,
				Description: string(sess.Status), Updated: sess.UpdatedAt,
				Favorite: sess.Favorite, Lexical: true, Peek: true,
			})
		}
		add("sessions", "Sessions", rows)
	}

	return groups, nil
}

// hitRow projects a retrieval hit into a result row: the snippet when the
// lexical leg quoted a match, the item's own description otherwise (a
// semantic-only hit has no matched term to quote).
func hitRow(h retrieve.Hit) searchRow {
	row := searchRow{
		Kind: h.Kind, ID: h.ID, Title: h.Title, Project: h.Project,
		Age: h.Age, Description: h.Description, Updated: h.Updated,
		Favorite: h.Favorite, Peek: true,
	}
	if h.Similarity > 0 {
		row.Similarity = min(int(h.Similarity*100+0.5), 100)
		row.ScoreTone = similarityTone(row.Similarity)
	}
	if h.Kind == "note" {
		row.Href = "/console/notes/" + h.ID
	} else {
		row.Href = "/console/memories/" + h.ID
	}
	if h.Snippet != "" {
		row.SnippetHTML = highlightSnippet(h.Snippet)
		row.Lexical = true
	}
	return row
}

func similarityTone(score int) string {
	switch {
	case score >= 70:
		return "strong"
	case score >= 45:
		return "good"
	default:
		return "related"
	}
}

// filterFavoriteGroups keeps only starred rows (the ?fav=1 control), dropping
// groups the filter empties. It runs after the candidate queries, so it narrows
// what surfaced rather than reaching deeper -- same contract as the page limit.
func filterFavoriteGroups(groups []searchGroup) []searchGroup {
	out := groups[:0]
	for _, g := range groups {
		kept := make([]searchRow, 0, len(g.Rows))
		for _, row := range g.Rows {
			if row.Favorite {
				kept = append(kept, row)
			}
		}
		if len(kept) > 0 {
			out = append(out, searchGroup{Kind: g.Kind, Label: g.Label, Count: len(kept), Rows: kept})
		}
	}
	return out
}

// sortSearchRows orders the unified page projection. Relevance preserves the
// grouped retrieval order -- knowledge is fused-ranked, the structured stores
// provide their own deterministic defaults -- except that starred rows float
// first (a stable partition, so fused order holds within both halves).
func sortSearchRows(rows []searchRow, sortKey string) {
	if sortKey == "relevance" {
		sort.SliceStable(rows, func(i, j int) bool {
			return rows[i].Favorite && !rows[j].Favorite
		})
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch sortKey {
		case "newest":
			return a.Updated.After(b.Updated)
		case "oldest":
			return a.Updated.Before(b.Updated)
		case "project":
			ap, bp := strings.ToLower(a.Project), strings.ToLower(b.Project)
			if ap != bp {
				return ap < bp // global (empty) sorts first
			}
			if !a.Updated.Equal(b.Updated) {
				return a.Updated.After(b.Updated)
			}
			return strings.ToLower(a.Title) < strings.ToLower(b.Title)
		case "confidence":
			aScored, bScored := a.Similarity > 0, b.Similarity > 0
			if aScored != bScored {
				return aScored
			}
			if a.Similarity != b.Similarity {
				return a.Similarity > b.Similarity
			}
			if a.Lexical != b.Lexical {
				return a.Lexical
			}
			return false
		default:
			return false // validated at the HTTP boundary
		}
	})
}

type searchWindow struct {
	Key   string
	Label string
	Since time.Time
}

type searchWindowOption struct {
	Key    string
	Label  string
	Active bool
}

func resolveSearchWindow(key string, now time.Time) searchWindow {
	switch key {
	case "24h":
		return searchWindow{Key: key, Label: "past 24 hours", Since: now.Add(-24 * time.Hour)}
	case "7d":
		return searchWindow{Key: key, Label: "past 7 days", Since: now.AddDate(0, 0, -7)}
	case "30d":
		return searchWindow{Key: key, Label: "past 30 days", Since: now.AddDate(0, 0, -30)}
	case "1y":
		return searchWindow{Key: key, Label: "past year", Since: now.AddDate(-1, 0, 0)}
	default:
		return searchWindow{Key: "all", Label: "all time"}
	}
}

func searchWindowOptions(active string) []searchWindowOption {
	labels := map[string]string{"24h": "24h", "7d": "7 days", "30d": "30 days", "1y": "1 year", "all": "All"}
	out := make([]searchWindowOption, 0, len(searchWindowKeys))
	for _, key := range searchWindowKeys {
		out = append(out, searchWindowOption{Key: key, Label: labels[key], Active: key == active})
	}
	return out
}

type searchSortOption struct {
	Key      string
	Label    string
	Selected bool
}

func searchSortOptions(active string) []searchSortOption {
	labels := map[string]string{
		"relevance": "Relevance", "newest": "Newest first", "oldest": "Oldest first",
		"project": "Project", "confidence": "Confidence",
	}
	out := make([]searchSortOption, 0, len(searchSortKeys))
	for _, key := range searchSortKeys {
		out = append(out, searchSortOption{Key: key, Label: labels[key], Selected: key == active})
	}
	return out
}

// searchScope is one entry in the page's scope selector.
type searchScope struct {
	Key    string
	Label  string
	Active bool
}

// searchScopeOptions builds the ordered selector entries, flagging the active
// one. Labels are title-cased scope keys, except "all".
func searchScopeOptions(active string) []searchScope {
	labels := map[string]string{
		"all": "All", "memories": "Memories", "notes": "Notes", "tasks": "Tasks",
		"plans": "Plans", "trials": "Trials", "projects": "Projects", "sessions": "Sessions",
	}
	out := make([]searchScope, 0, len(searchScopes))
	for _, k := range searchScopes {
		out = append(out, searchScope{Key: k, Label: labels[k], Active: k == active})
	}
	return out
}
