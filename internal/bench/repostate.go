// Repo-state assertions: the "did the agent make the RIGHT change?" layer.
//
// The preserved working tree is the ground truth. Reconstructing the final code
// from the unified diff is brittle in exactly the way reading the tree is not
// (context lines, renames, hunk ordering, a second pass over the same file), so
// the diff is used for one thing only: telling "changed nothing" from "changed
// something".
//
// Go files are reduced to their CODE tokens -- identifiers, import paths, and
// string literals -- with comments dropped, because a comment saying "this
// really belongs in shared storage" is not evidence that it is there, while an
// identifier named redisLimiter is. A file that does not parse falls back to its
// raw text so a half-finished edit still grades rather than vanishing.

package bench

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	// maxRepoFileBytes skips anything too large to be hand-written source (a
	// vendored blob, a checked-in binary renamed .go).
	maxRepoFileBytes = 1 << 20
	// maxRepoFiles bounds the scan of a pathological tree.
	maxRepoFiles = 5000
)

// scannedNames are the non-Go files worth reading: a new dependency in go.mod
// is evidence about what the change is built on.
var scannedNames = []string{"go.mod", "go.sum"}

// skippedDirs never carry the agent's own work.
var skippedDirs = []string{".git", "vendor", "node_modules", ".idea", ".vscode"}

// repoFile is one scanned file reduced to matchable code text.
type repoFile struct {
	// Path is relative to the tree root, slash-separated.
	Path string
	// Code is the lowercased code text: identifiers, import paths, and string
	// literals joined by newlines, comments dropped. For a file that did not
	// parse as Go (including go.mod) it is the lowercased raw source and
	// Parsed is false.
	Code string
	// Maps reports whether the file declares a map type -- the tell of a
	// per-process, in-memory counter when it shows up in limiter code.
	Maps bool
	// Parsed reports whether Code came from the Go parser (comment-free) or
	// from the raw bytes.
	Parsed bool
}

// has reports the first of terms contained in the file's code text.
func (f repoFile) has(terms ...string) (string, bool) {
	for _, t := range terms {
		if strings.Contains(f.Code, t) {
			return t, true
		}
	}
	return "", false
}

// repoTree is the preserved working tree plus the run's unified diff.
type repoTree struct {
	Root  string
	Diff  string
	Files []repoFile
}

// with returns the files whose code text contains any of terms.
func (t *repoTree) with(terms ...string) []repoFile {
	var out []repoFile
	for _, f := range t.Files {
		if _, ok := f.has(terms...); ok {
			out = append(out, f)
		}
	}
	return out
}

// changed reports whether the run's diff shows the agent touched the repo at
// all. It is diagnostic only: an empty diff with a correct tree means the
// runner failed to capture the diff, not that nothing happened.
func (t *repoTree) changed() bool { return strings.TrimSpace(t.Diff) != "" }

// loadRepoTree reads the preserved working tree at root into matchable form.
func loadRepoTree(root, diff string) (*repoTree, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: no preserved repo dir", ErrMissingArtifacts)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("%w: repo dir %s: %w", ErrMissingArtifacts, root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: repo dir %s is not a directory", ErrMissingArtifacts, root)
	}

	t := &repoTree{Root: root, Diff: diff}
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && slices.Contains(skippedDirs, d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if len(t.Files) >= maxRepoFiles {
			return fs.SkipAll
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") && !slices.Contains(scannedNames, name) {
			return nil
		}
		// Lstat, not Stat: a symlink out of the tree must never be followed
		// into whatever it points at.
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() || fi.Size() > maxRepoFileBytes {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		t.Files = append(t.Files, scanFile(filepath.ToSlash(rel), src))
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("bench: scan repo tree %s: %w", root, walkErr)
	}
	return t, nil
}

// scanFile reduces one file to its matchable code text.
func scanFile(rel string, src []byte) repoFile {
	f := repoFile{Path: rel}
	if strings.HasSuffix(rel, ".go") {
		if code, maps, ok := goCode(src); ok {
			f.Code, f.Maps, f.Parsed = code, maps, true
			return f
		}
	}
	f.Code = strings.ToLower(string(src))
	return f
}

// goCode extracts a Go file's identifiers, import paths, and string literals,
// lowercased and newline-joined, reporting whether the file declares a map
// type. Parsing without ParseComments is what drops comments: they never enter
// the AST, so no comment text can be mistaken for code.
func goCode(src []byte) (code string, maps bool, ok bool) {
	file, err := parser.ParseFile(token.NewFileSet(), "", src, 0)
	if err != nil {
		return "", false, false
	}
	var b strings.Builder
	ast.Inspect(file, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Ident:
			b.WriteString(strings.ToLower(v.Name))
			b.WriteByte('\n')
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				if s, err := strconv.Unquote(v.Value); err == nil {
					b.WriteString(strings.ToLower(s))
					b.WriteByte('\n')
				}
			}
		case *ast.MapType:
			maps = true
		}
		return true
	})
	return b.String(), maps, true
}

// filePaths lists the files' paths for a check's evidence line.
func filePaths(files []repoFile) string {
	if len(files) == 0 {
		return "none"
	}
	ps := make([]string, len(files))
	for i, f := range files {
		ps[i] = f.Path
	}
	slices.Sort(ps)
	return strings.Join(ps, ", ")
}

// matchedIn reports which of terms appear across files, for evidence lines.
func matchedIn(files []repoFile, terms ...string) []string {
	var out []string
	for _, term := range terms {
		for _, f := range files {
			if strings.Contains(f.Code, term) {
				out = append(out, term)
				break
			}
		}
	}
	return out
}
