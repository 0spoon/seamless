package retrieve

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// setIsolation registers a project and puts it in an isolation state. The
// projects-table row is what the fence reads (store.IsolationOf), so a slug
// that only ever appeared on memories is open by construction.
func setIsolation(t *testing.T, db *sql.DB, slug string, state core.Isolation) {
	t.Helper()
	ctx := context.Background()
	_, err := store.EnsureProject(ctx, db, slug, slug)
	require.NoError(t, err)
	require.NoError(t, store.SetProjectIsolation(ctx, db, slug, state))
}

// insFinding records one completed session with findings, for the briefing's
// own "Recent findings" and family "Sibling projects" sections.
func insFinding(t *testing.T, db *sql.DB, id, name, project, findings string) {
	t.Helper()
	now := time.Now()
	require.NoError(t, store.CreateSession(context.Background(), db, core.Session{
		ID: id, Name: name, ProjectSlug: project, Status: core.SessionCompleted,
		Findings: findings, CreatedAt: now, UpdatedAt: now,
	}))
}

const (
	sealedLine       = "Isolation: sealed -- this briefing contains only this project's knowledge (no global memories, no family)\n"
	confidentialLine = "Isolation: confidential -- writes outside this project are disabled\n"
)

// A sealed project is a clean room: its briefing carries its own knowledge and
// nothing else -- no global memories, no parent memories, no sibling findings
// or memories -- even with the family topology and the cross-over knobs saying
// otherwise. The topology guards normally prevent an isolated project from
// having family at all; these exclusions are the defense in depth against a
// stale row that outlived a tighten.
func TestBriefing_SealedDropsGlobalAndFamily(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/work/vault":"vault"}`))
	require.NoError(t, store.SetSetting(ctx, db, store.SettingProjectFamilies, `{"clients":["vault","atlas"]}`))

	// The link predates the fence on purpose: SetProjectParent refuses to build
	// one for an isolated project, so this is exactly the stale row a tighten
	// would have detached -- the case these exclusions defend against. vault
	// needs its projects-table row first, because SetProjectParent on an
	// unregistered slug affects 0 rows and would silently void the premise.
	_, err := store.EnsureProject(ctx, db, "shared", "Shared")
	require.NoError(t, err)
	_, err = store.EnsureProject(ctx, db, "vault", "vault")
	require.NoError(t, err)
	require.NoError(t, store.SetProjectParent(ctx, db, "vault", "shared", time.Now().UTC()))
	setIsolation(t, db, "vault", core.IsolationSealed)

	// Assert the premise. Every family check below is an absence check, so a
	// link that failed to land would make this test pass for the wrong reason.
	vault, ok, err := store.ProjectBySlug(ctx, db, "vault")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "shared", vault.ParentSlug, "the stale parent link must survive the fence")

	insMem(t, db, "01V", "gotcha", "vault-thing", "the sealed project's own memory", "vault")
	insMem(t, db, "01G", "reference", "global-fact", "applies everywhere", "")
	insMem(t, db, "01P", "reference", "shared-thing", "the shared parent's memory", "shared")
	insMem(t, db, "01A", "gotcha", "atlas-thing", "a sibling's memory", "atlas")
	insFinding(t, db, "01S", "cc/atlas", "atlas", "the atlas migration shipped")

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.IncludeSiblingMemories = true }))

	b, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/work/vault", Source: "startup"})
	require.NoError(t, err)
	require.Contains(t, b, "vault-thing", "the sealed project still sees its own knowledge")
	require.Contains(t, b, sealedLine)

	require.NotContains(t, b, "global-fact", "a sealed briefing drops the global scope")
	require.NotContains(t, b, "shared-thing", "a sealed briefing folds in no parent memories")
	require.NotContains(t, b, "atlas-thing", "a sealed briefing folds in no sibling memories")
	require.NotContains(t, b, "the atlas migration shipped", "a sealed briefing folds in no sibling findings")
	require.NotContains(t, b, "Sibling projects:")
	require.NotContains(t, b, "Sibling memories:")

	require.Contains(t, ids, "01V")
	require.NotContains(t, ids, "01G")
	require.NotContains(t, ids, "01P")
	require.NotContains(t, ids, "01A")
}

// Confidential fences outbound only, and family cross-over is bidirectional --
// so a confidential family member neither contributes to a sibling's briefing
// nor receives one's content, while the global memories it is entitled to keep
// flowing in.
func TestBriefing_ConfidentialFamilyExcludedBothDirections(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap,
		`{"/work/app":"app","/work/secret":"secret"}`))
	require.NoError(t, store.SetSetting(ctx, db, store.SettingProjectFamilies, `{"product":["app","secret"]}`))

	setIsolation(t, db, "secret", core.IsolationConfidential)

	insMem(t, db, "01APP", "gotcha", "app-thing", "the open project's memory", "app")
	insMem(t, db, "01SEC", "gotcha", "secret-thing", "the confidential project's memory", "secret")
	insMem(t, db, "01G", "reference", "global-fact", "applies everywhere", "")
	insFinding(t, db, "01S1", "cc/app", "app", "the app release went out")
	insFinding(t, db, "01S2", "cc/secret", "secret", "the client integration landed")

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.IncludeSiblingMemories = true }))

	t.Run("nothing leaves into a sibling's briefing", func(t *testing.T) {
		b, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/work/app", Source: "startup"})
		require.NoError(t, err)
		require.Contains(t, b, "app-thing")
		require.Contains(t, b, "global-fact")
		require.NotContains(t, b, "secret-thing")
		require.NotContains(t, b, "the client integration landed")
		require.NotContains(t, b, "Isolation:", "an open project's briefing carries no isolation line")
		require.NotContains(t, ids, "01SEC")
	})

	t.Run("and no family content enters the confidential one", func(t *testing.T) {
		b, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/work/secret", Source: "startup"})
		require.NoError(t, err)
		require.Contains(t, b, "secret-thing")
		require.Contains(t, b, confidentialLine)
		require.Contains(t, b, "global-fact",
			"confidential fences outbound only -- global memories still flow in")
		require.NotContains(t, b, "app-thing")
		require.NotContains(t, b, "the app release went out")
		require.NotContains(t, ids, "01APP")
	})
}

// The header line is what tells an agent inside the fence which world it is
// in. It renders directly under the "Seam project:" line on BOTH agent-facing
// briefings -- a subagent is inside the fence too, and its scope is already
// narrowed by the time it renders -- and an open project (the default) earns
// none on either.
func TestBriefing_IsolationLinePerState(t *testing.T) {
	tests := []struct {
		name  string
		state core.Isolation
		want  string
	}{
		{"open renders no line", core.IsolationOpen, ""},
		{"confidential names the write fence", core.IsolationConfidential, confidentialLine},
		{"sealed names the missing sections", core.IsolationSealed, sealedLine},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupDB(t)
			ctx := context.Background()
			require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))
			setIsolation(t, db, "p", tt.state)
			insMem(t, db, "01A", "gotcha", "a-memory", "keeps the briefing non-empty", "p")
			// The subagent briefing renders only where constraints do.
			insMem(t, db, "01C", "constraint", "a-constraint", "binding on the child too", "p")
			svc := New(db, nil, budgets(), nil)

			main, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
			require.NoError(t, err)
			sub, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", AgentType: "Explore"})
			require.NoError(t, err)
			require.Contains(t, sub, "(subagent scope)", "the child briefing rendered at all")

			if tt.want == "" {
				require.NotContains(t, main, "Isolation:")
				require.NotContains(t, sub, "Isolation:")
				return
			}
			// Pinned directly under each surface's header line: the counts
			// sentence on the main briefing, the scope tag on the subagent one.
			require.Contains(t, main, "recent findings.\n"+tt.want)
			require.Contains(t, sub, "(subagent scope).\n"+tt.want)
		})
	}
}

// isolationLine is the single renderer, so the exact strings are pinned here
// too: the briefing tests above assert placement, this asserts the copy.
func TestIsolationLine(t *testing.T) {
	require.Equal(t, "", isolationLine(core.IsolationOpen))
	require.Equal(t, confidentialLine, isolationLine(core.IsolationConfidential))
	require.Equal(t, sealedLine, isolationLine(core.IsolationSealed))
}

// The prompt matcher searches the same scope the briefing does, so a sealed
// project's <seam-recall> can only ever name its own memories. The open
// project is the control: same data, same query, and the global memory is the
// only thing that can match.
func TestPromptRecall_SealedMatchesOwnProjectOnly(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap,
		`{"/w/vault":"vault","/w/open":"openp"}`))
	setIsolation(t, db, "vault", core.IsolationSealed)

	// The only memory that can match the query lives in the global scope.
	insMem(t, db, "01G1", "gotcha", "chroma-boot-race", "chroma container health check startup race", "")
	insMem(t, db, "01G2", "reference", "ulid-ids", "use ulid identifiers not uuid values", "")
	insMem(t, db, "01G3", "decision", "wal-mode", "sqlite runs in write ahead logging mode", "")
	for _, p := range []string{"vault", "openp"} {
		insMem(t, db, "01"+p+"1", "gotcha", p+"-alpha", "an unrelated local topic", p)
		insMem(t, db, "01"+p+"2", "gotcha", p+"-beta", "another unrelated local topic", p)
		insMem(t, db, "01"+p+"3", "gotcha", p+"-gamma", "a third unrelated local topic", p)
	}

	svc := New(db, nil, budgets(), nil)
	const prompt = "why does the chroma container fail its health check"

	open, openIDs, err := svc.PromptRecall(ctx, "/w/open", prompt)
	require.NoError(t, err)
	require.Contains(t, open, "chroma-boot-race", "an open project matches global memories")
	require.Contains(t, openIDs, "01G1")

	sealed, sealedIDs, err := svc.PromptRecall(ctx, "/w/vault", prompt)
	require.NoError(t, err)
	require.Empty(t, sealed, "a sealed project's prompt corpus holds no global memories")
	require.Empty(t, sealedIDs)
}

// Recall narrows its scope for a sealed caller -- and only its scope: the
// fused ranking, the boosts, the budget, and the Hit payload are untouched
// (search-semantic-floor-scope).
func TestRecall_SealedProjectReturnsNoGlobalHits(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()

	insMem(t, db, "01V", "reference", "vault-thing", "shared searchable topic", "vault")
	insMem(t, db, "01O", "reference", "open-thing", "shared searchable topic", "openp")
	insMem(t, db, "01C", "reference", "client-thing", "shared searchable topic", "clientp")
	insMem(t, db, "01G", "reference", "global-thing", "shared searchable topic", "")

	setIsolation(t, db, "vault", core.IsolationSealed)
	setIsolation(t, db, "clientp", core.IsolationConfidential)

	svc := New(db, nil, budgets(), nil) // nil embedder => FTS-only

	names := func(t *testing.T, in RecallInput) []string {
		t.Helper()
		hits, err := svc.Recall(ctx, in)
		require.NoError(t, err)
		out := make([]string, len(hits))
		for i, h := range hits {
			out[i] = h.Name
		}
		return out
	}

	t.Run("open caller keeps the global scope", func(t *testing.T) {
		require.ElementsMatch(t, []string{"open-thing", "global-thing"},
			names(t, RecallInput{Query: "shared searchable topic", Project: "openp", Limit: 10}))
	})

	t.Run("sealed caller sees its own project only", func(t *testing.T) {
		require.ElementsMatch(t, []string{"vault-thing"},
			names(t, RecallInput{Query: "shared searchable topic", Project: "vault", Limit: 10}))
	})

	t.Run("confidential caller still reads global", func(t *testing.T) {
		require.ElementsMatch(t, []string{"client-thing", "global-thing"},
			names(t, RecallInput{Query: "shared searchable topic", Project: "clientp", Limit: 10}))
	})

	t.Run("browse mode shares the narrowed scope", func(t *testing.T) {
		require.ElementsMatch(t, []string{"vault-thing"},
			names(t, RecallInput{Kind: "reference", Project: "vault", Limit: 10}))
		require.ElementsMatch(t, []string{"open-thing", "global-thing"},
			names(t, RecallInput{Kind: "reference", Project: "openp", Limit: 10}))
	})
}
