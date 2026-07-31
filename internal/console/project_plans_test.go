package console

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// insertPlanNote seeds a note index row carrying a plan composition tag, which
// is what the workspace reads for a plan's status chip and star (the console
// test harness has no Files layer to write one through).
func insertPlanNote(t *testing.T, db *sql.DB, id, project, slug, title, tagsJSON string, ts time.Time) {
	t.Helper()
	stamp := core.FormatTime(ts)
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO notes_index
		    (id, title, slug, description, project, file_path, tags, content_hash, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, ?, ?, 'hash', ?, ?)`,
		id, title, slug, project, "notes/"+project+"/"+slug+".md", tagsJSON, stamp, stamp)
	require.NoError(t, err)
}

func TestGroupSteps_FoldsLongDoneRuns(t *testing.T) {
	at := func(min int) time.Time {
		return time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC).Add(time.Duration(min) * time.Minute)
	}
	done := func(title string, closed time.Time) planStepVM {
		return planStepVM{ID: title, Title: title, State: "done", Closed: closed}
	}
	live := func(title, state string) planStepVM {
		return planStepVM{ID: title, Title: title, State: state}
	}

	t.Run("a run at the threshold folds", func(t *testing.T) {
		groups := groupSteps([]planStepVM{
			live("W1", "doing"),
			done("W2", at(1)), done("W3", at(9)), done("W4", at(4)),
			live("W5", "open"),
		})
		require.Len(t, groups, 3)
		require.False(t, groups[0].DoneRun)
		require.True(t, groups[1].DoneRun)
		require.Len(t, groups[1].Steps, 3)
		require.Equal(t, "W3", groups[1].LastTitle, "the run is labeled by its most recently closed step")
		require.Equal(t, at(9), groups[1].LastClosed)
		require.False(t, groups[2].DoneRun)
	})

	t.Run("a short run stays expanded", func(t *testing.T) {
		groups := groupSteps([]planStepVM{done("W1", at(1)), done("W2", at(2)), live("W3", "open")})
		require.Len(t, groups, 3)
		for _, g := range groups {
			require.False(t, g.DoneRun)
			require.Len(t, g.Steps, 1)
		}
	})

	t.Run("every step closed folds into one group", func(t *testing.T) {
		groups := groupSteps([]planStepVM{done("W1", at(1)), done("W2", at(2)), done("W3", at(3)), done("W4", at(4))})
		require.Len(t, groups, 1)
		require.True(t, groups[0].DoneRun)
		require.Len(t, groups[0].Steps, 4)
	})

	t.Run("no steps", func(t *testing.T) {
		require.Empty(t, groupSteps(nil))
	})
}

func TestBlockingChain_DepthCapAndCycleGuard(t *testing.T) {
	db, _ := newConsole(t)
	svc, err := New(Config{DB: db, APIKey: testKey})
	require.NoError(t, err)
	ctx := context.Background()
	now := time.Now().UTC()

	// A -> B -> C -> D -> E, every one still open: the walk must stop at the
	// depth cap rather than reporting the whole spine.
	ids := make([]string, 5)
	for i := range ids {
		ids[i] = mustID(t)
	}
	for i := len(ids) - 1; i >= 0; i-- {
		var deps []string
		if i+1 < len(ids) {
			deps = []string{ids[i+1]}
		}
		require.NoError(t, store.CreateTask(ctx, db, core.Task{
			ID: ids[i], ProjectSlug: "demo", Title: "step" + string(rune('A'+i)), Status: core.TaskOpen,
			PlanSlug: "chain", DependsOn: deps, CreatedAt: now, UpdatedAt: now,
		}))
	}
	head, err := store.TaskByID(ctx, db, ids[0])
	require.NoError(t, err)

	pc := planStepCtx{cache: map[string]core.Task{}, onPage: map[string]bool{}, now: now}
	chain := svc.blockingChain(ctx, head, pc)
	require.Len(t, chain, planChainDepth, "the walk is depth-capped")
	require.Equal(t, "stepB", chain[0].Title, "nearest blocker first")

	// A cycle must terminate, not spin: point the tail back at the head.
	_, err = db.ExecContext(ctx, `INSERT INTO task_deps (task_id, depends_on) VALUES (?, ?)`, ids[4], ids[0])
	require.NoError(t, err)
	cyclic, err := store.TaskByID(ctx, db, ids[0])
	require.NoError(t, err)
	chain = svc.blockingChain(ctx, cyclic, planStepCtx{cache: map[string]core.Task{}, onPage: map[string]bool{}, now: now})
	require.LessOrEqual(t, len(chain), planChainDepth)
	seen := map[string]bool{}
	for _, c := range chain {
		require.False(t, seen[c.ID], "a cycle must not revisit a blocker")
		seen[c.ID] = true
	}
}

// TestProjectWorkspace_PlanCardIsLiveTriage covers the tab's whole live
// surface in one render: the plan header (status chip, star, claimable, moved),
// expandable step rows with a skipped dossier island, the dependency chain, the
// gardener staleness badge, and the transition ledger.
func TestProjectWorkspace_PlanCardIsLiveTriage(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.CreateProject(ctx, db, core.Project{
		ID: mustID(t), Slug: "seamless", Name: "Seamless", CreatedAt: now, UpdatedAt: now,
	}))
	sessID := mustID(t)
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: sessID, Name: "cc/hawk", ProjectSlug: "seamless", Status: core.SessionActive,
		CreatedAt: now, UpdatedAt: now,
	}))
	// The plan's narrative note: status chip + star come from here.
	insertPlanNote(t, db, mustID(t), "seamless", "cc-plan-ios", "iOS demo parity",
		`["cc-plan","plan:ios-demo","plan-status:approved"]`, now)

	step := func(title string, deps ...string) string {
		id := mustID(t)
		require.NoError(t, store.CreateTask(ctx, db, core.Task{
			ID: id, ProjectSlug: "seamless", Title: title, Status: core.TaskOpen,
			PlanSlug: "ios-demo", DependsOn: deps, CreatedAt: now, UpdatedAt: now,
		}))
		return id
	}
	registry := step("registry entries")
	launcher := step("partner launcher", registry)
	step("hardware pass", launcher)
	step("copy tuning") // unblocked: the plan's one claimable step
	_, err := store.ClaimTask(ctx, db, registry, sessID, 15*time.Minute, now)
	require.NoError(t, err)
	// A transition the ledger must read back with its actor.
	_, err = db.ExecContext(ctx, `INSERT INTO events (id, ts, kind, session_id, project_slug, item_id, payload)
		VALUES (?, ?, ?, ?, 'seamless', ?, ?)`,
		mustID(t), core.FormatTime(now), string(core.EventTaskTransition), sessID, registry,
		`{"to":"in_progress","claimed_by":"`+sessID+`"}`)
	require.NoError(t, err)
	// A pending gardener settlement makes the plan stale.
	_, err = store.CreateProposal(ctx, db, store.ProposalShipPlan, map[string]any{
		"slug": "ios-demo", "project": "seamless", "title": "iOS demo parity",
	})
	require.NoError(t, err)

	body := getHTMLBody(t, mux, "/console/projects/seamless?tab=tasks")

	require.Contains(t, body, `id="plan-ios-demo"`, "the card is keyed so a morph reorders instead of rewriting")
	require.Contains(t, body, `href="/console/plans/ios-demo"`, "the header links to the plan")
	require.Contains(t, body, "approved", "the narrative note's status chip")
	require.Contains(t, body, `action="/console/favorites/plan/ios-demo"`, "the plan star")
	require.Contains(t, body, "1 ready", "claimable steps are the headline number")
	require.Contains(t, body, "moved ", "last activity")
	require.Contains(t, body, "stale?", "the pending gardener settlement badge")
	require.Contains(t, body, `href="/console/gardener"`)

	require.Contains(t, body, `<details class="step doing" id="`+registry+`">`, "step rows are expandable")
	require.Contains(t, body, `id="dossier-`+registry+`" data-live-skip data-dossier="/console/tasks/`+registry+`"`,
		"the dossier is a client-owned island the morph never clobbers")
	require.Contains(t, body, `data-lease-exp=`, "the claim carries a machine-readable expiry for the ticker")
	require.Contains(t, body, "cc/hawk", "the holder")
	require.Contains(t, body, `href="#`+registry+`" data-jump`, "a blocker on this page is a jump, not a navigation")
	require.Contains(t, body, "unblocks 1", "the reverse edge")
	require.Contains(t, body, "claimed", "the ledger verb")
}

// TestProjectWorkspace_LapsedClaimOffersIntervention covers the row an owner
// actually has to act on: in_progress, still naming a holder, lease expired.
// core.Task.ClaimLive reports false there, so the cue has to come off the raw
// fields (constraint claimtask-claimless-inprogress-and-lease-boundary).
func TestProjectWorkspace_LapsedClaimOffersIntervention(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, store.CreateProject(ctx, db, core.Project{
		ID: mustID(t), Slug: "seamless", Name: "Seamless", CreatedAt: now, UpdatedAt: now,
	}))
	sessID := mustID(t)
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: sessID, Name: "cc/ghost", ProjectSlug: "seamless", Status: core.SessionActive,
		CreatedAt: now, UpdatedAt: now,
	}))
	id := mustID(t)
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: id, ProjectSlug: "seamless", Title: "stranded step", Status: core.TaskOpen,
		PlanSlug: "ghosted", CreatedAt: now, UpdatedAt: now,
	}))
	// Claim it in the past with a short lease, so the lease is already gone.
	_, err := store.ClaimTask(ctx, db, id, sessID, time.Minute, now.Add(-time.Hour))
	require.NoError(t, err)
	// A second, still-open step keeps the plan active.
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: mustID(t), ProjectSlug: "seamless", Title: "next up", Status: core.TaskOpen,
		PlanSlug: "ghosted", CreatedAt: now, UpdatedAt: now,
	}))

	body := getHTMLBody(t, mux, "/console/projects/seamless?tab=tasks")
	require.Contains(t, body, "cc/ghost", "a lapsed claim still names who holds it")
	require.Contains(t, body, "lease lapsed", "the intervention cue")
	require.Contains(t, body, `action="/console/tasks/`+id+`/release"`, "the inline release form")
	require.Contains(t, body, `name="next" value="/console/projects/seamless?tab=tasks"`,
		"releasing returns to this workspace, not the tasks library")
	// The strip must sit outside the row: a form nested in a <summary> would
	// toggle the row on every click.
	require.NotContains(t, body, `<summary class="step-row">`+"\n"+`    <form`)
	require.Regexp(t, `</details>\s*<div class="step-alert">`, body)
}

func TestProjectWorkspace_FoldsDoneRunsAndCompletedPlans(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, store.CreateProject(ctx, db, core.Project{
		ID: mustID(t), Slug: "seamless", Name: "Seamless", CreatedAt: now, UpdatedAt: now,
	}))

	// Distinct creation stamps: the timeline renders newest-created first, so
	// the three closed steps land as one consecutive run below the open one.
	seq := 0
	seed := func(plan, title string, status core.TaskStatus) string {
		seq++
		at := now.Add(time.Duration(seq) * time.Minute)
		id := mustID(t)
		require.NoError(t, store.CreateTask(ctx, db, core.Task{
			ID: id, ProjectSlug: "seamless", Title: title, Status: core.TaskOpen,
			PlanSlug: plan, CreatedAt: at, UpdatedAt: at,
		}))
		if status != core.TaskOpen {
			_, err := store.UpdateTask(ctx, db, id, store.TaskPatch{Status: &status}, "", at)
			require.NoError(t, err)
		}
		return id
	}
	first := seed("live", "one", core.TaskDone)
	seed("live", "two", core.TaskDone)
	seed("live", "three", core.TaskDone)
	seed("live", "four", core.TaskOpen)
	seed("older", "wrapped up", core.TaskDone)

	body := getHTMLBody(t, mux, "/console/projects/seamless?tab=tasks")
	require.Contains(t, body, `<details class="step-run" id="run-`, "three closed steps in a row fold away")
	require.Contains(t, body, "3 completed steps")
	require.Contains(t, body, first, "the folded rows are still rendered inside")
	require.Contains(t, body, `id="plans-completed"`, "the finished plan gets a rollup group")
	require.Contains(t, body, "1 completed plan")
	require.Contains(t, body, `href="/console/plans/older"`)
	require.NotContains(t, body, `id="plan-older"`, "a finished plan costs no step query and renders no card")
}

func TestTaskRelease_ReturnsToTheSurfaceThatAskedFor(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()
	sessID := mustID(t)
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: sessID, Name: "cc/otter", ProjectSlug: "demo", Status: core.SessionActive,
		CreatedAt: now, UpdatedAt: now,
	}))

	release := func(t *testing.T, next string) string {
		t.Helper()
		id := mustID(t)
		require.NoError(t, store.CreateTask(ctx, db, core.Task{
			ID: id, ProjectSlug: "demo", Title: "held", Status: core.TaskOpen,
			PlanSlug: "p", CreatedAt: now, UpdatedAt: now,
		}))
		_, err := store.ClaimTask(ctx, db, id, sessID, time.Minute, now)
		require.NoError(t, err)
		form := url.Values{}
		if next != "" {
			form.Set("next", next)
		}
		req := httptest.NewRequest(http.MethodPost, "/console/tasks/"+id+"/release", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer "+testKey)
		rr := do(mux, req)
		require.Equal(t, http.StatusSeeOther, rr.Code, rr.Body.String())
		return rr.Header().Get("Location")
	}

	t.Run("no next keeps today's redirect", func(t *testing.T) {
		require.Contains(t, release(t, ""), "/console/tasks/")
	})
	t.Run("a console next is honored with the flash appended", func(t *testing.T) {
		loc := release(t, "/console/projects/demo?tab=tasks")
		require.True(t, strings.HasPrefix(loc, "/console/projects/demo?tab=tasks&notice="), loc)
	})
	t.Run("an off-console next falls back to the task", func(t *testing.T) {
		loc := release(t, "https://evil.example/steal")
		require.True(t, strings.HasPrefix(loc, "/console/tasks/"), loc)
	})
}

func TestTaskPeek_CarriesTheCallersNextIntoItsActions(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()
	sessID := mustID(t)
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: sessID, Name: "cc/otter", ProjectSlug: "demo", Status: core.SessionActive,
		CreatedAt: now, UpdatedAt: now,
	}))
	id := mustID(t)
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: id, ProjectSlug: "demo", Title: "held", Status: core.TaskOpen,
		PlanSlug: "p", CreatedAt: now, UpdatedAt: now,
	}))
	_, err := store.ClaimTask(ctx, db, id, sessID, time.Minute, now)
	require.NoError(t, err)

	body := getHTMLBody(t, mux, "/console/tasks/"+id+"?peek=1&next="+url.QueryEscape("/console/projects/demo?tab=tasks"))
	require.Contains(t, body, `name="next" value="/console/projects/demo?tab=tasks"`,
		"an embedded dossier's release + star post back to the surface that opened it")
	require.Contains(t, body, `action="/console/favorites/task/`+id+`"`)

	// Without a next the fragment keeps its own defaults.
	plain := getHTMLBody(t, mux, "/console/tasks/"+id+"?peek=1")
	require.Contains(t, plain, `name="next" value="/console/tasks/`+id+`"`)
	require.NotContains(t, plain, "evil")
}

// TestProjectTasksPanel_StaysInsideTheDocument is the structural half of
// console-data-updates-never-reload-document for the panel's own client.
func TestProjectTasksPanel_StaysInsideTheDocument(t *testing.T) {
	source, err := templateFS.ReadFile("templates/projectdetail.html")
	require.NoError(t, err)
	page := string(source)
	for _, banned := range []string{"location.reload(", "location.assign(", "location.href ="} {
		require.NotContains(t, page, banned,
			"the plans panel must patch in place, never navigate as a fetch fallback")
	}
	require.Contains(t, page, "The row is unchanged.", "a failed dossier fetch says so in place")
	require.Contains(t, page, "seam:content-updated",
		"the skipped dossier island must re-sync after a mutation patch")
	require.Contains(t, page, "'toggle'", "the dossier loads lazily on first open")
}
