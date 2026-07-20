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
	"slices"
	"strings"

	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

// searchScopes are the accepted ?scope values: "all", the two knowledge kinds
// retrieve.Search understands, and one per structured entity.
var searchScopes = []string{"all", "memories", "notes", "tasks", "plans", "trials", "projects", "sessions"}

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
	Peek        bool          `json:"peek"`
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
	Query  string        `json:"query"`
	Scope  string        `json:"scope"`
	Fast   bool          `json:"fast"`
	Groups []searchGroup `json:"groups"`
	Total  int           `json:"total"`
	Scopes []searchScope `json:"-"` // the page's scope selector; not part of the JSON contract
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

func (s *Service) searchPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if n := []rune(q); len(n) > searchQueryMax {
		q = strings.TrimSpace(string(n[:searchQueryMax]))
	}
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "all"
	}
	if !slices.Contains(searchScopes, scope) {
		s.badRequest(w, r, fmt.Sprintf("invalid scope %q: valid values are %s",
			scope, strings.Join(searchScopes, ", ")))
		return
	}
	fastParam := r.URL.Query().Get("fast")
	if fastParam != "" && fastParam != "1" {
		s.badRequest(w, r, fmt.Sprintf("invalid fast %q: valid values are 1 (or omit)", fastParam))
		return
	}
	fast := fastParam == "1"

	data := searchData{Query: q, Scope: scope, Fast: fast, Scopes: searchScopeOptions(scope)}
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
	data.Groups = groups
	for _, g := range groups {
		data.Total += g.Count
	}
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
			Query: data.Query, Scope: scope, Limit: limit, Semantic: !data.Fast,
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
		tasks, err := store.SearchTasks(ctx, s.cfg.DB, data.Query, limit)
		if err != nil {
			return nil, err
		}
		rows := make([]searchRow, 0, len(tasks))
		for _, t := range tasks {
			rows = append(rows, searchRow{
				Kind: "task", ID: t.ID, Title: t.Title, Project: t.ProjectSlug,
				Age: ago(t.UpdatedAt), Href: "/console/tasks/" + t.ID,
				Description: string(t.Status), Peek: true,
			})
		}
		add("tasks", "Tasks", rows)
	}

	if data.wants("plans") {
		plans, err := store.SearchPlans(ctx, s.cfg.DB, data.Query, limit)
		if err != nil {
			return nil, err
		}
		rows := make([]searchRow, 0, len(plans))
		for _, p := range plans {
			rows = append(rows, searchRow{
				Kind: "plan", ID: p.Slug, Title: p.Title, Project: p.Project,
				Age: ago(p.Updated), Href: "/console/plans/" + p.Slug, Peek: true,
			})
		}
		add("plans", "Plans", rows)
	}

	if data.wants("trials") {
		trials, err := store.SearchTrials(ctx, s.cfg.DB, data.Query, limit)
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
				Description: desc, Peek: true,
			})
		}
		add("trials", "Trials", rows)
	}

	if data.wants("projects") {
		projects, err := store.SearchProjects(ctx, s.cfg.DB, data.Query, limit)
		if err != nil {
			return nil, err
		}
		rows := make([]searchRow, 0, len(projects))
		for _, p := range projects {
			rows = append(rows, searchRow{
				Kind: "project", ID: p.Slug, Title: p.Slug, Age: ago(p.UpdatedAt),
				Href: "/console/projects/" + p.Slug, Description: p.Name, Peek: true,
			})
		}
		add("projects", "Projects", rows)
	}

	if data.wants("sessions") {
		sessions, err := store.SearchSessions(ctx, s.cfg.DB, data.Query, limit)
		if err != nil {
			return nil, err
		}
		rows := make([]searchRow, 0, len(sessions))
		for _, sess := range sessions {
			rows = append(rows, searchRow{
				Kind: "session", ID: sess.ID, Title: sess.Name, Project: sess.ProjectSlug,
				Age: ago(sess.UpdatedAt), Href: "/console/sessions/" + sess.ID,
				Description: string(sess.Status), Peek: true,
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
		Age: h.Age, Description: h.Description, Peek: true,
	}
	if h.Kind == "note" {
		row.Href = "/console/notes/" + h.ID
	} else {
		row.Href = "/console/memories/" + h.ID
	}
	if h.Snippet != "" {
		row.SnippetHTML = highlightSnippet(h.Snippet)
	}
	return row
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
