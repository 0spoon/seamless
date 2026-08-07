package mcp_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/files"
)

// content_hash is the ETag half of the expect_hash precondition: an agent can
// only say "write only if nothing moved since I read this" if the read handed it
// the hash. It must be the digest of the FILE's bytes -- not of the body, not of
// the index row -- because that is what the precondition compares against inside
// the mutation lock, and a frontmatter-only edit (a star, a tag) has to move it.
func TestMemoryRead_ReturnsFileContentHash(t *testing.T) {
	ctx := context.Background()
	url, _, mgr, dir := newServerFiles(t, nil)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "name": "hash-sess"})

	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "hashed", "kind": "reference", "description": "d", "body": "first body",
	})
	relPath := files.MemoryRelPath("demo", "hashed")
	absPath := filepath.Join(dir, filepath.FromSlash(relPath))

	// Hash the raw file the same way the reconciler does, so this asserts the
	// contract rather than agreeing with whatever the read happened to compute.
	rawHash := func() string {
		t.Helper()
		raw, err := os.ReadFile(absPath) //nolint:gosec // test-owned path under t.TempDir()
		require.NoError(t, err)
		return files.ContentHash(string(raw))
	}

	read := callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "hashed"})
	first, ok := read["content_hash"].(string)
	require.True(t, ok, "memory_read must return content_hash, or expect_hash has nothing to compare against")
	require.NotEmpty(t, first)
	require.Equal(t, rawHash(), first)

	// It also matches what the files layer itself reports for that path.
	onDisk, err := mgr.Store().ReadMemory(relPath)
	require.NoError(t, err)
	require.Equal(t, onDisk.ContentHash, first)

	// A body change moves it.
	callJSON(t, ctx, cli, "memory_append", map[string]any{"name": "hashed", "body": "a second line"})
	afterAppend := callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "hashed"})
	require.Equal(t, rawHash(), afterAppend["content_hash"])
	require.NotEqual(t, first, afterAppend["content_hash"], "an append must move the hash")

	// So does a frontmatter-only change: the star lives in the file, and an agent
	// holding a pre-star hash is holding a stale file.
	callJSON(t, ctx, cli, "favorite_set", map[string]any{"kind": "memory", "id": "hashed", "favorite": true})
	afterStar := callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "hashed"})
	require.Equal(t, rawHash(), afterStar["content_hash"])
	require.NotEqual(t, afterAppend["content_hash"], afterStar["content_hash"],
		"a frontmatter-only edit must move the hash too")

	// Reading by id is the same read and must carry the same handle.
	byID := callJSON(t, ctx, cli, "memory_read", map[string]any{"id": read["id"]})
	require.Equal(t, afterStar["content_hash"], byID["content_hash"])
}
