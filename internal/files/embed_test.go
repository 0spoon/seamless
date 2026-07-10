package files

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/store"
)

// keywordVocab is the fixed vocabulary the fake embedder counts over, so that
// texts sharing words produce similar vectors (a stand-in for real semantics).
var keywordVocab = []string{"chroma", "boot", "race", "outage", "note", "seam", "supersession"}

func keywordVec(text string) []float32 {
	text = strings.ToLower(text)
	vec := make([]float32, len(keywordVocab))
	for i, w := range keywordVocab {
		vec[i] = float32(strings.Count(text, w))
	}
	return vec
}

type fakeEmbedder struct {
	model string
	fail  bool
}

func (f *fakeEmbedder) Model() string { return f.model }
func (f *fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if f.fail {
		return nil, errors.New("embedder down")
	}
	return keywordVec(text), nil
}

func TestEmbedOnWrite(t *testing.T) {
	m, db := newManager(t)
	m.SetEmbedder(&fakeEmbedder{model: "fake-v1"})
	ctx := context.Background()

	written, err := m.WriteMemory(ctx, sampleMemory())
	require.NoError(t, err)

	// A vector row was written under the embedder's model.
	var model string
	var dims int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT model, dims FROM embeddings WHERE item_id = ?`, written.ID).Scan(&model, &dims))
	require.Equal(t, "fake-v1", model)
	require.Equal(t, len(keywordVocab), dims)

	// Cosine search finds it for a semantically-matching query.
	hits, err := store.CosineSearch(ctx, db, keywordVec("chroma boot race"), "fake-v1", nil, 10)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, written.ID, hits[0].ItemID)
}

// Two items rank by similarity to the query.
func TestEmbedRanking(t *testing.T) {
	m, db := newManager(t)
	m.SetEmbedder(&fakeEmbedder{model: "fake-v1"})
	ctx := context.Background()

	boot := sampleMemory() // name/desc/body full of "chroma"/"boot"/"race"
	bootW, err := m.WriteMemory(ctx, boot)
	require.NoError(t, err)

	other := sampleMemory()
	other.ID = "01K0MEMORY000000000000000B"
	other.Name = "outage-note"
	other.Description = "an outage note about seam"
	other.Body = "outage note seam\n"
	otherW, err := m.WriteMemory(ctx, other)
	require.NoError(t, err)

	hits, err := store.CosineSearch(ctx, db, keywordVec("chroma boot race"), "fake-v1", nil, 10)
	require.NoError(t, err)
	require.Len(t, hits, 2)
	require.Equal(t, bootW.ID, hits[0].ItemID, "the chroma/boot/race memory ranks first")
	require.Equal(t, otherW.ID, hits[1].ItemID)
}

// Embedding is best-effort: a failing embedder must not fail the write, and the
// item stays indexed for FTS.
func TestEmbedBestEffortOnFailure(t *testing.T) {
	m, db := newManager(t)
	m.SetEmbedder(&fakeEmbedder{model: "fake-v1", fail: true})
	ctx := context.Background()

	written, err := m.WriteMemory(ctx, sampleMemory())
	require.NoError(t, err) // write still succeeds

	require.Equal(t, 1, indexedCount(t, db, "memories_index")) // still indexed
	var n int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embeddings WHERE item_id=?`, written.ID).Scan(&n))
	require.Zero(t, n) // but no vector
}

// With no embedder configured, writes and reconcile work and produce no vectors.
func TestNoEmbedderNoVectors(t *testing.T) {
	m, db := newManager(t)
	ctx := context.Background()
	_, err := m.WriteMemory(ctx, sampleMemory())
	require.NoError(t, err)
	require.Zero(t, indexedCount(t, db, "embeddings"))
}

// Deleting a file removes its vector alongside the index + fts rows.
func TestDeleteRemovesVector(t *testing.T) {
	m, db := newManager(t)
	m.SetEmbedder(&fakeEmbedder{model: "fake-v1"})
	ctx := context.Background()

	written, err := m.WriteMemory(ctx, sampleMemory())
	require.NoError(t, err)
	require.Equal(t, 1, indexedCount(t, db, "embeddings"))

	require.NoError(t, m.Remove(ctx, written.FilePath))
	require.Zero(t, indexedCount(t, db, "embeddings"))
	require.Zero(t, indexedCount(t, db, "memories_index"))
}

// Reconcile embeds files it indexes from disk.
func TestReconcileEmbeds(t *testing.T) {
	m, db := newManager(t)
	m.SetEmbedder(&fakeEmbedder{model: "fake-v1"})
	ctx := context.Background()
	_, err := m.store.WriteMemory(sampleMemory()) // straight to disk
	require.NoError(t, err)
	require.NoError(t, m.Reconcile(ctx))
	require.Equal(t, 1, indexedCount(t, db, "embeddings"))
}
