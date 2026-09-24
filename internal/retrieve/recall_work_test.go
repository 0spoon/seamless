package retrieve

import (
	"context"
	"testing"
	"time"

	"database/sql"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// seedWorkRecord writes one task, one trial, and one ended session, all sharing
// a distinctive term so a single query reaches every kind. Every write goes
// through the real store writers, so these tests also pin that the writers
// index at all.
func seedWorkRecord(t *testing.T, db *sql.DB, project string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: "01WTASK", ProjectSlug: project, Title: "retire the flywheel poller",
		Body:   "dropped: the flywheel poller duplicated the watcher",
		Status: core.TaskDropped, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, store.CreateTrial(ctx, db, core.Trial{
		ID: "01WTRIAL", Lab: "flywheel", Title: "poller interval sweep",
		Expected: "flywheel settles under 200ms", Actual: "flywheel oscillated",
		Outcome: core.OutcomePartial, ProjectSlug: project, CreatedAt: now,
	}))
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: "01WSESS", Name: "cc/flywheel", ProjectSlug: project,
		Status: core.SessionActive, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, store.UpdateSession(ctx, db, core.Session{
		ID: "01WSESS", Name: "cc/flywheel", ProjectSlug: project,
		Status: core.SessionCompleted, Findings: "the flywheel work is superseded by the watcher",
		UpdatedAt: now,
	}))
}

func hitsByKind(hits []Hit) map[string]Hit {
	out := make(map[string]Hit, len(hits))
	for _, h := range hits {
		out[h.Kind] = h
	}
	return out
}

// The default scope reaches the work record. This is the whole point: an agent
// asking "what happened to the flywheel" should not have to already suspect the
// answer was written as a task rather than a memory.
func TestRecallDefaultScopeSpansTheWorkRecord(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	insMem(t, db, "01WMEM", "decision", "flywheel-retired", "the flywheel poller was retired", "seam")
	seedWorkRecord(t, db, "seam")

	svc := New(db, nil, budgets(), nil) // nil embedder => FTS-only

	hits, err := svc.Recall(ctx, RecallInput{Query: "flywheel", Project: "seam", Limit: 20})
	require.NoError(t, err)

	byKind := hitsByKind(hits)
	for _, kind := range []string{core.ItemKindMemory, core.ItemKindTask, core.ItemKindTrial, core.ItemKindSession} {
		require.Contains(t, byKind, kind, "scope=all must reach %s", kind)
	}

	// A work-record hit announces its lifecycle word, which is what separates a
	// step still worth claiming from a decision only worth learning from.
	require.Equal(t, "dropped", byKind[core.ItemKindTask].Status)
	require.Equal(t, "partial", byKind[core.ItemKindTrial].Status)
	require.Equal(t, "completed", byKind[core.ItemKindSession].Status)
	// Knowledge kinds have no such word and must keep the pre-existing payload.
	require.Empty(t, byKind[core.ItemKindMemory].Status)

	// A session's findings ARE the payload: there is no session_read to follow
	// the hit up with.
	require.Contains(t, byKind[core.ItemKindSession].Description, "superseded by the watcher")
	require.Equal(t, "flywheel", byKind[core.ItemKindTrial].Name, "the lab is the trial's handle")
}

func TestRecallNarrowScopesSelectOneKind(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	insMem(t, db, "01WMEM", "decision", "flywheel-retired", "the flywheel poller was retired", "seam")
	seedWorkRecord(t, db, "seam")
	svc := New(db, nil, budgets(), nil)

	for scope, wantKind := range map[string]string{
		"memories": core.ItemKindMemory,
		"tasks":    core.ItemKindTask,
		"trials":   core.ItemKindTrial,
		"sessions": core.ItemKindSession,
	} {
		hits, err := svc.Recall(ctx, RecallInput{Query: "flywheel", Project: "seam", Scope: scope, Limit: 20})
		require.NoError(t, err, scope)
		require.NotEmpty(t, hits, "scope=%s found nothing", scope)
		for _, h := range hits {
			require.Equal(t, wantKind, h.Kind, "scope=%s leaked a %s", scope, h.Kind)
		}
	}
}

// The kind filter names a memory frontmatter kind, so pairing it with a scope
// that excludes memories can only ever match nothing. Every such scope is
// rejected by name rather than returning a misleading empty result.
func TestRecallKindRejectsEveryNonMemoryScope(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	svc := New(db, nil, budgets(), nil)

	for _, scope := range []string{"notes", "tasks", "trials", "sessions"} {
		_, err := svc.Recall(ctx, RecallInput{Query: "x", Kind: "convention", Scope: scope})
		require.Error(t, err, scope)
		require.Contains(t, err.Error(), scope)
	}
	// The scopes that still contain memories stay legal.
	for _, scope := range []string{"", "all", "memories"} {
		_, err := svc.Recall(ctx, RecallInput{Query: "x", Kind: "convention", Scope: scope})
		require.NoError(t, err, scope)
	}
}

// The isolation fence is enforced inside the candidate query on fts.project, so
// it covers the work record for free -- but "for free" is exactly the kind of
// claim that needs a test.
func TestRecallWorkRecordRespectsProjectScope(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	seedWorkRecord(t, db, "other")
	svc := New(db, nil, budgets(), nil)

	hits, err := svc.Recall(ctx, RecallInput{Query: "flywheel", Project: "seam", Limit: 20})
	require.NoError(t, err)
	require.Empty(t, hits, "another project's work record must not be visible")

	hits, err = svc.Recall(ctx, RecallInput{Query: "flywheel", Project: "other", Limit: 20})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
}

// A plan's steps are the half of a composition that notes do not cover, so a
// plan-step task has to be reachable by its slug.
func TestRecallFindsPlanStepsByPlanSlug(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: "01STEP", ProjectSlug: "seam", Title: "ship the retrieval screen",
		Status: core.TaskOpen, PlanSlug: "search-visibility", CreatedAt: now, UpdatedAt: now,
	}))
	svc := New(db, nil, budgets(), nil)

	hits, err := svc.Recall(ctx, RecallInput{Query: "search-visibility", Project: "seam", Limit: 10})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, core.ItemKindTask, hits[0].Kind)
	require.Equal(t, "search-visibility", hits[0].Name)
	require.Equal(t, "ship the retrieval screen", hits[0].Title)
}

// Console Search keeps its own per-entity sections built from the structured
// tables, so its fused leg must stay knowledge-only: widening it would report
// the same task twice on one page, in two differently-ranked places.
func TestSearchStaysKnowledgeOnly(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	insMem(t, db, "01WMEM", "decision", "flywheel-retired", "the flywheel poller was retired", "seam")
	seedWorkRecord(t, db, "seam")
	svc := New(db, nil, budgets(), nil)

	hits, err := svc.Search(ctx, SearchInput{Query: "flywheel", Limit: 20})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	for _, h := range hits {
		require.Contains(t, core.KnowledgeItemKinds, h.Kind, "console search leaked a %s", h.Kind)
	}
}

func TestRecallScopeKindsCoverEveryDeclaredScope(t *testing.T) {
	// Every scope the boundaries advertise must select something; a scope that
	// fell through to the default would silently search everything, which is
	// the failure the enum exists to prevent.
	for _, scope := range RecallScopes {
		kinds := recallScopeKinds(scope)
		require.NotEmpty(t, kinds, scope)
		if scope != "all" {
			require.Len(t, kinds, 1, "scope %q must name exactly one kind", scope)
		}
	}
	require.ElementsMatch(t,
		append(append([]string{}, core.KnowledgeItemKinds...), core.WorkItemKinds...),
		recallScopeKinds("all"))
}
