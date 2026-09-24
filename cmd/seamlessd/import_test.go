package main

import (
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// `seamlessd import` fronts two unrelated operations, and which one runs is
// decided by what --from names on disk -- never by a flag. These pin the
// dispatch and the refusal that keeps a flag from one family from being
// silently ignored by the other.

func TestClassifyImportSource(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "instance.tar.gz")
	require.NoError(t, os.WriteFile(file, []byte("not really a tarball"), 0o600))
	subdir := filepath.Join(dir, "seam-v1")
	require.NoError(t, os.MkdirAll(subdir, 0o700))

	tests := []struct {
		name     string
		from     string
		wantPath string
		wantKind importSource
		wantErr  string
	}{
		{name: "a directory is the v1 store", from: subdir, wantPath: subdir, wantKind: sourceV1Dir},
		{name: "a regular file is an archive", from: file, wantPath: file, wantKind: sourceArchive},
		{name: "dash is an archive on stdin", from: "-", wantPath: "-", wantKind: sourceArchive},
		{name: "empty is refused", from: "   ", wantErr: "--from is empty"},
		{name: "missing is refused", from: filepath.Join(dir, "nope"), wantErr: "nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, kind, err := classifyImportSource(tt.from)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantPath, got)
			require.Equal(t, tt.wantKind, kind)
		})
	}
}

// A leading ~ expands before the stat, so `--from ~/.seam` dispatches on the
// real directory rather than on a literal "~/.seam" that never exists.
func TestClassifyImportSource_ExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	got, kind, err := classifyImportSource("~")
	require.NoError(t, err)
	require.Equal(t, home, got)
	require.Equal(t, sourceV1Dir, kind)
}

func TestCheckImportFlagFamily(t *testing.T) {
	tests := []struct {
		name    string
		kind    importSource
		set     map[string]bool
		wantErr string
	}{
		{name: "v1 with its own flags", kind: sourceV1Dir, set: map[string]bool{"from": true, "skip": true, "embed": true}},
		{name: "archive with its own flags", kind: sourceArchive, set: map[string]bool{"dry-run": true, "force": true, "embed": true}},
		{name: "v1 with --dry-run", kind: sourceV1Dir, set: map[string]bool{"dry-run": true}, wantErr: "--dry-run does not apply to a v1 data directory"},
		{name: "v1 with --force", kind: sourceV1Dir, set: map[string]bool{"force": true}, wantErr: "--force does not apply to a v1 data directory"},
		{name: "v1 with both", kind: sourceV1Dir, set: map[string]bool{"dry-run": true, "force": true}, wantErr: "--dry-run, --force"},
		{name: "archive with --skip", kind: sourceArchive, set: map[string]bool{"skip": true}, wantErr: "--skip does not apply to an archive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkImportFlagFamily(tt.kind, tt.set)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// The family lists are checked against the real flag set rather than a
// transcribed copy: a renamed flag would otherwise leave a guard that matches
// nothing and fails open (AGENTS.md > Common pitfalls).
func TestImportFlagFamiliesNameRealFlags(t *testing.T) {
	fs, _ := newImportFlagSet()
	defined := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) { defined[f.Name] = true })

	for _, name := range append(append([]string{}, v1OnlyFlags...), archiveOnlyFlags...) {
		require.True(t, defined[name], "family list names %q, which import does not define", name)
	}
	// Every family flag belongs to exactly one family.
	for _, name := range v1OnlyFlags {
		require.NotContains(t, archiveOnlyFlags, name)
	}
}

// The round trip through both verbs: export an instance, restore it into an
// empty data dir, then re-import the same archive into the now-populated one and
// get a merge that inserts nothing. This is the manual acceptance run minus the
// live daemon -- which is exactly the part a test cannot have, so the
// daemon-running refusal stays a manual check.
//
// --embed=false throughout: an embed pass would be a provider round trip, and a
// unit test never hits a real service (AGENTS.md > Testing).
func TestImportArchive_RestoreThenIdempotentMerge(t *testing.T) {
	src := throwawayInstance(t)
	archivePath := filepath.Join(t.TempDir(), "instance.tar.gz")
	require.NoError(t, runExport([]string{"-o", archivePath}))

	dest := filepath.Join(t.TempDir(), "restored")
	t.Setenv("SEAMLESS_DATA_DIR", dest)

	// Fresh: an absent destination is a restore, not a merge.
	out := captureStdout(t, func() error {
		return runImport([]string{"--from", archivePath, "-embed=false"})
	})
	require.Contains(t, out, "import (fresh)")
	require.FileExists(t, filepath.Join(dest, "seam.db"))
	require.NoFileExists(t, filepath.Join(dest, "seam.db.import-tmp"),
		"the snapshot is renamed into place last; nothing may be left under the temp name")
	for _, rel := range []string{"memory/_global/user-prefers-uv.md", "notes/seam/export-design.md"} {
		want, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		require.NoError(t, err)
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
		require.NoError(t, err)
		require.Equal(t, want, got, "a fresh restore is byte-exact for %s", rel)
	}

	before := treeHashes(t, dest)

	// Dry run against the now-populated destination: reports a merge, writes
	// nothing.
	out = captureStdout(t, func() error {
		return runImport([]string{"--from", archivePath, "-embed=false", "-dry-run"})
	})
	require.Contains(t, out, "import (merge, dry run)")
	require.Equal(t, before, treeHashes(t, dest), "a dry run must not write a byte")

	// Real merge: every id is already here, so nothing is inserted.
	out = captureStdout(t, func() error {
		return runImport([]string{"--from", archivePath, "-embed=false"})
	})
	require.Contains(t, out, "import (merge)")
	require.Contains(t, out, "0 memories, 0 notes")
	require.Contains(t, out, "2 skipped, already present")
	require.Contains(t, out, "sessions 0")
	require.Contains(t, out, "tasks 0")
	require.Equal(t, before, treeHashes(t, dest), "a re-merge of the same archive rewrites nothing")
}

// treeHashes maps every markdown path under dir to its contents, so a test can
// assert that a run changed none of them.
func treeHashes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, tree := range []string{"memory", "notes"} {
		root := filepath.Join(dir, tree)
		require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			body, readErr := os.ReadFile(p)
			if readErr != nil {
				return readErr
			}
			rel, relErr := filepath.Rel(dir, p)
			if relErr != nil {
				return relErr
			}
			out[filepath.ToSlash(rel)] = string(body)
			return nil
		}))
	}
	require.NotEmpty(t, out)
	return out
}

// flagsSet must report presence, not value: `--embed=false` and an omitted
// --embed both leave the parsed value false, and only the former is "present".
func TestFlagsSetReportsPresenceNotValue(t *testing.T) {
	fs, f := newImportFlagSet()
	require.NoError(t, fs.Parse([]string{"-embed=false"}))
	require.False(t, f.embed)
	require.Equal(t, map[string]bool{"embed": true}, flagsSet(fs))

	fs, _ = newImportFlagSet()
	require.NoError(t, fs.Parse(nil))
	require.Empty(t, flagsSet(fs))
}
