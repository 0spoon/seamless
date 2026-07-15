package console

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/store"
)

// noteSortKeys are the accepted ?sort values on the notes list.
var noteSortKeys = []string{"recent", "name"}

// noteRow is a display projection of a note for the browser.
type noteRow struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Project     string    `json:"project"`
	Tags        []string  `json:"tags,omitempty"`
	Updated     time.Time `json:"updated"`
}

// noteProjectGroup is one project's notes, for the grouped browser (project ""
// is global).
type noteProjectGroup struct {
	Project string    `json:"project"`
	Count   int       `json:"count"`
	Notes   []noteRow `json:"notes"`
}

// notesData is the payload for the Notes browser.
type notesData struct {
	Groups []noteProjectGroup `json:"groups"`
	Count  int                `json:"count"`
	Query  string             `json:"query,omitempty"`
	Sort   string             `json:"sort"`
}

func (s *Service) notesList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sortKey := r.URL.Query().Get("sort")
	if sortKey == "" {
		sortKey = "recent"
	}
	if !slices.Contains(noteSortKeys, sortKey) {
		s.badRequest(w, r, fmt.Sprintf("invalid sort %q: valid values are %s", sortKey, strings.Join(noteSortKeys, ", ")))
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	q := strings.ToLower(query)
	notes, err := store.ListNotes(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	byProject := map[string][]noteRow{}
	matched := 0
	for _, n := range notes {
		row := noteRow{
			ID: n.ID, Title: n.Title, Description: n.Description,
			Project: n.Project, Tags: n.Tags, Updated: n.Updated,
		}
		if !noteMatches(row, q) {
			continue
		}
		byProject[n.Project] = append(byProject[n.Project], row)
		matched++
	}
	s.render(w, r, "notes", pageData{
		Title:  "Notes",
		Active: "notes",
		Data:   notesData{Groups: buildNoteGroups(byProject, sortKey), Count: matched, Query: query, Sort: sortKey},
	})
}

// noteMatches reports whether a note row satisfies the ?q text filter (empty q
// matches all): a case-insensitive substring of title, description, or any tag.
func noteMatches(row noteRow, q string) bool {
	if q == "" {
		return true
	}
	if strings.Contains(strings.ToLower(row.Title), q) ||
		strings.Contains(strings.ToLower(row.Description), q) {
		return true
	}
	for _, t := range row.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}

// buildNoteGroups orders the project->notes map: global ("") first, then
// projects alphabetically. Within a group notes keep ListNotes' newest-first
// order for sort=recent, or sort by title for sort=name.
func buildNoteGroups(byProject map[string][]noteRow, sortKey string) []noteProjectGroup {
	projects := make([]string, 0, len(byProject))
	for p := range byProject {
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool {
		if (projects[i] == "") != (projects[j] == "") {
			return projects[i] == "" // global first
		}
		return projects[i] < projects[j]
	})
	groups := make([]noteProjectGroup, 0, len(projects))
	for _, p := range projects {
		rows := byProject[p]
		if sortKey == "name" {
			sort.SliceStable(rows, func(i, j int) bool {
				return strings.ToLower(rows[i].Title) < strings.ToLower(rows[j].Title)
			})
		}
		groups = append(groups, noteProjectGroup{Project: p, Count: len(rows), Notes: rows})
	}
	return groups
}
