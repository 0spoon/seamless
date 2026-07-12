package console

import (
	"net/http"
	"sort"
	"time"

	"github.com/0spoon/seamless/internal/store"
)

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
// is the inbox).
type noteProjectGroup struct {
	Project string    `json:"project"`
	Count   int       `json:"count"`
	Notes   []noteRow `json:"notes"`
}

// notesData is the payload for the Notes browser.
type notesData struct {
	Groups []noteProjectGroup `json:"groups"`
	Count  int                `json:"count"`
}

func (s *Service) notesList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	notes, err := store.ListNotes(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	byProject := map[string][]noteRow{}
	for _, n := range notes {
		byProject[n.Project] = append(byProject[n.Project], noteRow{
			ID: n.ID, Title: n.Title, Description: n.Description,
			Project: n.Project, Tags: n.Tags, Updated: n.Updated,
		})
	}
	s.render(w, r, "notes", pageData{
		Title:  "Notes",
		Active: "notes",
		Data:   notesData{Groups: buildNoteGroups(byProject), Count: len(notes)},
	})
}

// buildNoteGroups orders the project->notes map: the inbox ("") first, then
// projects alphabetically; notes within a group keep ListNotes' newest-first
// order.
func buildNoteGroups(byProject map[string][]noteRow) []noteProjectGroup {
	projects := make([]string, 0, len(byProject))
	for p := range byProject {
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool {
		if (projects[i] == "") != (projects[j] == "") {
			return projects[i] == "" // inbox first
		}
		return projects[i] < projects[j]
	})
	groups := make([]noteProjectGroup, 0, len(projects))
	for _, p := range projects {
		rows := byProject[p]
		groups = append(groups, noteProjectGroup{Project: p, Count: len(rows), Notes: rows})
	}
	return groups
}
