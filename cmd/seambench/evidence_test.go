package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvidence_WriteAndRemoveRoundTrip(t *testing.T) {
	repo := t.TempDir()
	remove, err := writeEvidence(repo, map[string]string{
		"logs/app.log":     "evidence\n",
		"logs/sub/more.md": "more\n",
		"toplevel.txt":     "x\n",
	})
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(repo, "logs", "app.log"))
	require.FileExists(t, filepath.Join(repo, "logs", "sub", "more.md"))
	require.FileExists(t, filepath.Join(repo, "toplevel.txt"))

	require.NoError(t, remove())
	require.NoFileExists(t, filepath.Join(repo, "toplevel.txt"))
	require.NoDirExists(t, filepath.Join(repo, "logs"), "created directories are pruned")
}

func TestEvidence_EmptyMapIsANoOp(t *testing.T) {
	remove, err := writeEvidence(t.TempDir(), nil)
	require.NoError(t, err)
	require.NoError(t, remove())
}

func TestEvidence_RefusesAPathThatEscapesTheRepo(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(filepath.Dir(repo), "escaped")
	remove, err := writeEvidence(repo, map[string]string{"../escaped": "x"})
	require.ErrorContains(t, err, "escapes the repo")
	require.NoError(t, remove())
	require.NoFileExists(t, outside)
}

// Colliding with a fixture file would turn scaffolding into a graded
// modification (or delete the fixture file on removal).
func TestEvidence_RefusesAnExistingPath(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repo, "auth.go"), []byte("package main\n"), 0o644))

	remove, err := writeEvidence(repo, map[string]string{"auth.go": "overwrite"})
	require.ErrorContains(t, err, "already exists")
	require.NoError(t, remove())
	b, err := os.ReadFile(filepath.Join(repo, "auth.go"))
	require.NoError(t, err)
	require.Equal(t, "package main\n", string(b), "the fixture file survives untouched")
}

// A partial write still cleans up what it managed to write. Paths are written
// in sorted order, so the colliding later path leaves the earlier one behind
// for the remover.
func TestEvidence_PartialWriteCleansUp(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repo, "z-exists.log"), []byte("x"), 0o644))

	remove, err := writeEvidence(repo, map[string]string{
		"a/first.log":  "one",
		"z-exists.log": "collides",
	})
	require.Error(t, err)
	require.FileExists(t, filepath.Join(repo, "a", "first.log"))
	require.NoError(t, remove())
	require.NoDirExists(t, filepath.Join(repo, "a"))
	require.FileExists(t, filepath.Join(repo, "z-exists.log"))
}

func TestEvidence_ToleratesTheAgentDeletingIt(t *testing.T) {
	repo := t.TempDir()
	remove, err := writeEvidence(repo, map[string]string{"logs/app.log": "x"})
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(repo, "logs", "app.log")))
	require.NoError(t, remove())
}

// A directory the agent put its own work into is not pruned: that work
// belongs to the diff.
func TestEvidence_KeepsADirectoryTheAgentWroteInto(t *testing.T) {
	repo := t.TempDir()
	remove, err := writeEvidence(repo, map[string]string{"logs/app.log": "x"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "logs", "analysis.md"), []byte("agent work"), 0o644))

	require.NoError(t, remove())
	require.NoFileExists(t, filepath.Join(repo, "logs", "app.log"))
	require.FileExists(t, filepath.Join(repo, "logs", "analysis.md"))
}
