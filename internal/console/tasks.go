package console

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// taskRow is a display projection of a task.
type taskRow struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Project  string     `json:"project"`
	Status   string     `json:"status"`
	Deps     int        `json:"deps"`
	Created  time.Time  `json:"created"`
	Closed   *time.Time `json:"closed,omitempty"`
	Blockers []blocker  `json:"blockers,omitempty"`
}

type blocker struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// tasksData is the payload for the Tasks page.
type tasksData struct {
	Ready      []taskRow `json:"ready"`
	InProgress []taskRow `json:"inProgress"`
	Blocked    []taskRow `json:"blocked"`
	Closed     []taskRow `json:"closed"`
	ClosedMore int       `json:"closedMore"`
}

const closedLimit = 25

func (s *Service) tasks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	ready, err := store.AllReadyTasks(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	inProgress, err := store.AllTasksByStatus(ctx, s.cfg.DB, core.TaskInProgress)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	blocked, err := store.AllBlockedTasks(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	done, err := store.AllTasksByStatus(ctx, s.cfg.DB, core.TaskDone)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	dropped, err := store.AllTasksByStatus(ctx, s.cfg.DB, core.TaskDropped)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	closed := append(done, dropped...)
	sort.Slice(closed, func(i, j int) bool {
		return closedBefore(closed[j], closed[i]) // newest-closed first
	})
	closedMore := 0
	if len(closed) > closedLimit {
		closedMore = len(closed) - closedLimit
		closed = closed[:closedLimit]
	}

	data := tasksData{
		Ready:      taskRows(ready),
		InProgress: taskRows(inProgress),
		Blocked:    blockedRows(blocked),
		Closed:     taskRows(closed),
		ClosedMore: closedMore,
	}
	s.render(w, r, "tasks", pageData{Title: "Tasks", Active: "tasks", Data: data})
}

// taskRelease is the owner override for a claimed task: it force-releases the
// lock (reopening the task for any agent to claim) regardless of who holds it.
// It is reachable only from this owner surface (the console button and the
// bearer-authenticated `seam task release --force`), never the agent MCP tools.
func (s *Service) taskRelease(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	released, err := store.ForceReleaseTask(ctx, s.cfg.DB, id, time.Now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			s.notFound(w, r, "No task with id "+id+".")
			return
		}
		// Not in progress (nothing to release) or a store error: surface it
		// rather than pretend success.
		s.serverError(w, r, err)
		return
	}
	if s.cfg.Events != nil {
		if _, err := s.cfg.Events.Record(ctx, core.Event{
			Kind: core.EventTaskTransition, ProjectSlug: released.ProjectSlug, ItemID: released.ID,
			Payload: map[string]any{"to": string(released.Status), "released": true, "by": "console"},
		}); err != nil {
			s.logger.Warn("console: record task release event", "error", err)
		}
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, taskDetailJSON(released))
		return
	}
	http.Redirect(w, r, "/console/tasks/"+released.ID, http.StatusSeeOther)
}

// taskDetailJSON is the minimal task view returned to a CLI/JSON caller of
// taskRelease, so `seam task release --force` can print the reopened task.
func taskDetailJSON(t core.Task) map[string]any {
	return map[string]any{
		"id": t.ID, "title": t.Title, "project": t.ProjectSlug, "status": string(t.Status),
	}
}

// closedBefore reports whether a closed before b (by ClosedAt, falling back to
// UpdatedAt so tasks with a missing close time still order sensibly).
func closedBefore(a, b core.Task) bool {
	return closedAt(a).Before(closedAt(b))
}

func closedAt(t core.Task) time.Time {
	if t.ClosedAt != nil {
		return *t.ClosedAt
	}
	return t.UpdatedAt
}

func taskRows(tasks []core.Task) []taskRow {
	out := make([]taskRow, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskRow{
			ID: t.ID, Title: t.Title, Project: t.ProjectSlug, Status: string(t.Status),
			Deps: len(t.DependsOn), Created: t.CreatedAt, Closed: t.ClosedAt,
		})
	}
	return out
}

func blockedRows(blocked []store.BlockedTask) []taskRow {
	out := make([]taskRow, 0, len(blocked))
	for _, b := range blocked {
		row := taskRow{
			ID: b.Task.ID, Title: b.Task.Title, Project: b.Task.ProjectSlug,
			Status: string(b.Task.Status), Deps: len(b.Task.DependsOn), Created: b.Task.CreatedAt,
		}
		for _, bl := range b.Blockers {
			row.Blockers = append(row.Blockers, blocker{ID: bl.ID, Title: bl.Title, Status: string(bl.Status)})
		}
		out = append(out, row)
	}
	return out
}
