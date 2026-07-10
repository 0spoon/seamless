package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/llm"
	"github.com/0spoon/seamless/internal/store"
)

// TestOllamaSemanticSearch is an opt-in end-to-end check that the files + llm +
// store pipeline returns semantically sane cosine results with a real Ollama
// embedder. It is skipped unless SEAMLESS_OLLAMA_TEST=1 (so `make test` stays
// hermetic). Run with:
//
//	SEAMLESS_OLLAMA_TEST=1 go test ./internal/files/ -run TestOllamaSemanticSearch -v
func TestOllamaSemanticSearch(t *testing.T) {
	if os.Getenv("SEAMLESS_OLLAMA_TEST") == "" {
		t.Skip("set SEAMLESS_OLLAMA_TEST=1 to run the live Ollama embedding test")
	}

	cfg := config.Defaults().LLM.Ollama
	embedder := llm.NewOllamaEmbedder(cfg.BaseURL, cfg.EmbeddingModel)

	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	m, err := NewManager(dir, db, nil)
	require.NoError(t, err)
	m.SetEmbedder(embedder)
	ctx := context.Background()

	mems := []struct{ name, desc, body string }{
		{"chroma-boot-race", "the chroma vector-database container answers its health check before it can serve queries, causing boot failures",
			"Docker starts the sidecar and the daemon together; add a readiness gate so the daemon waits for chroma to actually serve.\n"},
		{"ulid-over-uuid", "use ULID identifiers everywhere instead of UUID so ids sort lexicographically by creation time",
			"ULIDs embed a millisecond timestamp prefix; never use UUID v4 for primary keys here.\n"},
		{"sqlite-wal-mode", "enable WAL journal mode and a busy_timeout on every sqlite connection",
			"Set journal_mode=WAL and foreign_keys=on via the DSN so every pooled connection inherits them.\n"},
	}
	for _, mm := range mems {
		id, err := core.NewID()
		require.NoError(t, err)
		_, err = m.WriteMemory(ctx, core.Memory{
			ID: id, Kind: core.KindGotcha, Name: mm.name, Description: mm.desc,
			Project: "seam", Body: mm.body,
			Created: time.Now().UTC(), Updated: time.Now().UTC(), ValidFrom: time.Now().UTC(),
		})
		require.NoError(t, err)
	}

	// A query about a container failing to start should rank the boot-race memory first.
	qvec, err := embedder.Embed(ctx, "the database sidecar container fails to start up in time")
	require.NoError(t, err)
	hits, err := store.CosineSearch(ctx, db, qvec, embedder.Model(), nil, 3)
	require.NoError(t, err)
	require.NotEmpty(t, hits)

	var topName string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT name FROM memories_index WHERE id = ?`, hits[0].ItemID).Scan(&topName))
	require.Equal(t, "chroma-boot-race", topName, "boot-race memory should be the closest match; ranking=%v", hits)
	t.Logf("ranking: %+v", hits)
}
