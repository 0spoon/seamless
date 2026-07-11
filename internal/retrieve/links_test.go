package retrieve

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecall_LinkExpansion(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()

	// hit1 matches the query; neighbor does not, but hit1's body links to it.
	insMem(t, db, "hit1", "gotcha", "readiness-gate", "readiness gate fix", "seam")
	insMem(t, db, "nb", "gotcha", "chroma-boot-race", "chroma boot race", "seam")

	svc := New(db, nil, budgets(), nil)
	svc.SetBodyReader(fakeBodyReader{
		"memory/x/readiness-gate.md": "The fix: see [[chroma-boot-race]] for details.",
	})

	hits, err := svc.Recall(ctx, RecallInput{Query: "readiness gate", Project: "seam", Limit: 10})
	require.NoError(t, err)

	bySource := map[string]string{} // name -> source
	for _, h := range hits {
		bySource[h.Name] = h.Source
	}
	require.Contains(t, bySource, "readiness-gate")
	require.Contains(t, bySource, "chroma-boot-race", "linked neighbor pulled in")
	require.Equal(t, "link", bySource["chroma-boot-race"], "neighbor surfaced only via the link")
}

func TestRecall_LinkExpansion_NoBodyReaderIsNoop(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	insMem(t, db, "hit1", "gotcha", "readiness-gate", "readiness gate fix", "seam")
	insMem(t, db, "nb", "gotcha", "chroma-boot-race", "chroma boot race", "seam")

	svc := New(db, nil, budgets(), nil) // no body reader

	hits, err := svc.Recall(ctx, RecallInput{Query: "readiness gate", Project: "seam", Limit: 10})
	require.NoError(t, err)
	for _, h := range hits {
		require.NotEqual(t, "chroma-boot-race", h.Name, "no link expansion without a body reader")
	}
}
