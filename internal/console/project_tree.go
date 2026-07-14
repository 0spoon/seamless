package console

// The relations tree: plan -> step -> claiming session -> memory produced.
// Built per project and shared by two screens -- the project-detail Relations tab
// (project_detail.go) and the standalone /console/relations screen
// (relations.go) -- so it lives on its own rather than in either caller.

import (
	"context"
	"fmt"
	"html/template"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// treeNode is one node of the relations tree (plan -> step -> claiming session
// -> memory). Cap is server-built HTML (status chips, edges); Kids nest under it.
type treeNode struct {
	Lead string // plan|task|sess|mem -> CSS lead-<x>
	Icon string
	Name string
	Href string
	Cap  template.HTML
	Flag bool // an active/live session (adds .tflag)
	Kids []treeNode
}

// buildProjectTree assembles the relations tree for a project: each plan (active
// or completed) expands into its steps, each step into its claiming session, and
// each session into the memories it produced. Shared with the /console/relations
// screen.
func (s *Service) buildProjectTree(ctx context.Context, project string) ([]treeNode, error) {
	slugs, err := store.DistinctPlanSlugsForProject(ctx, s.cfg.DB, project)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	cache := map[string]core.Task{} // task IDs are global, so one cache spans plans
	var out []treeNode
	for _, plan := range slugs {
		steps, err := store.ListTasksForPlan(ctx, s.cfg.DB, project, "", plan)
		if err != nil {
			return nil, err
		}
		for _, st := range steps {
			cache[st.ID] = st
		}
		done := 0
		for _, st := range steps {
			if st.Status == core.TaskDone || st.Status == core.TaskDropped {
				done++
			}
		}
		node := treeNode{
			Lead: "plan", Icon: "git-merge", Name: "plan:" + plan,
			Cap: template.HTML(fmt.Sprintf(`&middot; %d steps &middot; %d%% done`,
				len(steps), percent(done, len(steps)))),
		}
		for _, st := range steps {
			node.Kids = append(node.Kids, s.treeStep(ctx, st, cache, now))
		}
		out = append(out, node)
	}
	return out, nil
}

// treeStep builds a step node and its claiming-session / memory subtree. An open
// step whose dependency is still unfinished carries a "blocked by" edge in its
// caption, so the tree reads the dependency spine like the mock's relations view.
func (s *Service) treeStep(ctx context.Context, t core.Task, cache map[string]core.Task, now time.Time) treeNode {
	step := treeNode{Lead: "task", Icon: taskTreeIcon(string(t.Status)), Name: t.Title}
	capHTML := kindChip(string(t.Status), taskTone(string(t.Status)))
	if t.Status == core.TaskOpen {
		// Only a named blocker earns an edge -- there is nothing to label otherwise.
		if blocker, _ := s.blockingDep(ctx, t, cache); blocker != "" {
			capHTML += ` <span class="edge" style="padding:0 6px">&larr; blocked by ` +
				template.HTMLEscapeString(blocker) + `</span>`
		}
	}
	step.Cap = template.HTML(capHTML)
	if t.ClaimLive(now) {
		if sess, ok, err := store.SessionByID(ctx, s.cfg.DB, t.ClaimedBy); err == nil && ok {
			step.Kids = append(step.Kids, s.treeSession(ctx, sess))
		}
	}
	return step
}

// blockingDep reports whether an open step has an unfinished dependency (open or
// in-progress) and, if so, returns that dependency's title. Both blocked-ness
// renderers go through it -- the relations tree's "blocked by" edge and the plan
// timeline's blocked row (planStep) -- so the two cannot disagree about what
// blocks a step. cache is shared across the whole walk, so a dependency resolves
// at most once per page.
//
// blocked is separate from the title because they are not the same question: a
// blocker with an empty title still blocks (the timeline greys the row), but the
// tree has no name to render an edge for and skips it.
func (s *Service) blockingDep(ctx context.Context, t core.Task, cache map[string]core.Task) (title string, blocked bool) {
	for _, depID := range t.DependsOn {
		dep, ok := cache[depID]
		if !ok {
			d, err := store.TaskByID(ctx, s.cfg.DB, depID)
			if err != nil {
				continue
			}
			dep, cache[depID] = d, d
		}
		if dep.Status == core.TaskOpen || dep.Status == core.TaskInProgress {
			return dep.Title, true
		}
	}
	return "", false
}

// treeSession builds a claiming-session node and its produced-memory leaves.
func (s *Service) treeSession(ctx context.Context, sess core.Session) treeNode {
	node := treeNode{
		Lead: "sess", Icon: "terminal", Name: sess.Name,
		Href: "/console/sessions/" + sess.ID,
	}
	if sess.Status == core.SessionActive {
		node.Flag = true
		node.Cap = template.HTML(`<span class="live-dot"></span>active`)
	} else {
		node.Cap = template.HTML(template.HTMLEscapeString(sess.Source) + " &middot; closed " + ago(sess.UpdatedAt))
	}
	mems, err := store.MemoriesForSession(ctx, s.cfg.DB, sess.Name)
	if err != nil {
		return node
	}
	for _, m := range mems {
		leaf := treeNode{
			Lead: "mem", Icon: "brain", Name: m.Name,
			Href: "/console/memories/" + m.ID,
			Cap:  template.HTML(kindChip(string(m.Kind), "")),
		}
		node.Kids = append(node.Kids, leaf)
	}
	return node
}

// taskTreeIcon maps a task status to its relations-tree glyph.
func taskTreeIcon(status string) string {
	switch status {
	case "done":
		return "check"
	case "in_progress":
		return "loader"
	case "dropped":
		return "circle"
	default:
		return "circle"
	}
}

// kindChip renders a compact status/kind chip for a tree caption.
func kindChip(text, tone string) string {
	cls := "kind"
	if tone != "" {
		cls += " " + tone
	}
	return `<span class="` + cls + `" style="padding:0 6px">` + template.HTMLEscapeString(text) + `</span>`
}
