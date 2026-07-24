package retrieve

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The kind filter is applied inside the candidate queries: only memories of
// that frontmatter kind return, and notes are excluded even under the default
// all scope -- a kind names a memory attribute, so it implies memories-only.
func TestRecallKindFilterMemoriesOnly(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	insMem(t, db, "01CON", "convention", "wordmark-markup", "wordmark markup sync rule", "seam")
	insMem(t, db, "01GOT", "gotcha", "wordmark-cache", "wordmark cache surprise", "seam")
	insNote(t, db, "01NOT", "wordmark-note", "Wordmark research", "seam", "[]", time.Now())
	_, err := db.ExecContext(ctx, `
		INSERT INTO fts (item_id, kind, project, title, name, description, body)
		VALUES ('01NOT', 'note', 'seam', 'Wordmark research', '', 'wordmark findings', 'wordmark findings')`)
	require.NoError(t, err)

	svc := New(db, nil, budgets(), nil) // nil embedder => FTS-only

	hits, err := svc.Recall(ctx, RecallInput{Query: "wordmark", Project: "seam", Kind: "convention", Limit: 5})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "wordmark-markup", hits[0].Name)

	// The same query unfiltered fuses all three items.
	all, err := svc.Recall(ctx, RecallInput{Query: "wordmark", Project: "seam", Limit: 5})
	require.NoError(t, err)
	require.Len(t, all, 3)
}

// scope=notes plus a kind filter is a contradiction: nothing could ever match,
// so it is rejected loudly instead of returning a misleading empty result. An
// unknown kind is rejected on the same grounds.
func TestRecallKindScopeAndValidity(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	svc := New(db, nil, budgets(), nil)

	_, err := svc.Recall(ctx, RecallInput{Query: "anything", Scope: "notes", Kind: "convention"})
	require.ErrorContains(t, err, "scope=notes")

	_, err = svc.Recall(ctx, RecallInput{Query: "anything", Kind: "conventionz"})
	require.ErrorContains(t, err, "unknown kind")

	// scope=memories plus kind is redundant but coherent, so it works.
	insMem(t, db, "01CON", "convention", "layout-fact", "where things deploy", "seam")
	hits, err := svc.Recall(ctx, RecallInput{Query: "deploy layout", Project: "seam", Scope: "memories", Kind: "convention"})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "layout-fact", hits[0].Name)
}
