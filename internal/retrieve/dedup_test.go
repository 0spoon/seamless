package retrieve

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/store"
)

// DedupHint is the sharpest instance of the starved-candidate-depth class,
// because its depth is 3 rather than recall's 24 and because the thing most
// similar to a proposed memory is very often the retired revision it is about to
// duplicate. Three superseded near-identical vectors used to fill the window and
// be discarded afterwards, so the hint came back empty at exactly the moment a
// live duplicate existed -- the one case the hint is for.
func TestDedupHint_SupersededDoesNotStarveLiveDuplicate(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()

	// Three retired revisions, each a perfect match for the query vector.
	for i := range 3 {
		id := fmt.Sprintf("OLD%027d", i)
		insMem(t, db, id, "gotcha", fmt.Sprintf("chroma-boot-race-v%02d", i), "old revision", "seam")
		require.NoError(t, store.UpsertEmbedding(ctx, db, id, "memory", "test-embed", []float32{1, 0, 0}))
		supersede(t, db, id, "01LIVE")
	}
	// The live memory a new write would genuinely duplicate: similar enough to
	// clear dedupMinScore, but ranked below all three retired revisions.
	insMem(t, db, "01LIVE", "gotcha", "chroma-boot-race", "the live duplicate", "seam")
	require.NoError(t, store.UpsertEmbedding(ctx, db, "01LIVE", "memory", "test-embed", []float32{0.99, 0.141, 0}))

	svc := New(db, fixedEmbedder{vec: []float32{1, 0, 0}}, budgets(), nil)

	hit, err := svc.DedupHint(ctx, "seam", "chroma-boot-race", "the live duplicate")
	require.NoError(t, err)
	require.NotNil(t, hit, "a live duplicate must be hinted even when superseded revisions outrank it")
	require.Equal(t, "chroma-boot-race", hit.Name)
	require.GreaterOrEqual(t, hit.Score, dedupMinScore)
}

// The advisory contract still holds in the other direction: when the only close
// matches are retired, there is nothing live to duplicate and the hint stays
// silent rather than pointing at knowledge that has already been replaced.
func TestDedupHint_SupersededOnlyYieldsNoHint(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()

	insMem(t, db, "01OLD", "gotcha", "chroma-boot-race-v00", "old revision", "seam")
	require.NoError(t, store.UpsertEmbedding(ctx, db, "01OLD", "memory", "test-embed", []float32{1, 0, 0}))
	supersede(t, db, "01OLD", "01NEW")

	svc := New(db, fixedEmbedder{vec: []float32{1, 0, 0}}, budgets(), nil)

	hit, err := svc.DedupHint(ctx, "seam", "chroma-boot-race", "a new revision")
	require.NoError(t, err)
	require.Nil(t, hit, "a superseded memory is not a duplicate worth flagging")
}
