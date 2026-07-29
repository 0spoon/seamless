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
//
// The same reduction is recorded at three granularities -- the whole file, each
// function declaration, each simple statement -- because "the tree contains this
// token" is not always the question. When the right change and the wrong change
// land in the same FILE (myapp's HTML handler and its static-asset route are
// both in server.go), only the finer levels can tell them apart: a function says
// where a change landed, and a statement says what a helper was applied to at
// its call site.

package bench

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// repoDecl is one named declaration -- a function, or a top-level const/var
// binding -- reduced to matchable code text.
type repoDecl struct {
	// Name is the declared name, lowercased. A method's receiver is not part
	// of it, so handleRefresh is "handlerefresh" whoever it hangs off.
	Name string
	// Code is the declaration's code text, reduced exactly like the file's.
	// Nested function literals are included, so a middleware's closure belongs
	// to the function that returns it.
	Code string
}

// repoFile is one scanned file reduced to matchable code text.
type repoFile struct {
	// Path is relative to the tree root, slash-separated.
	Path string
	// Code is the lowercased code text: identifiers, import paths, and string
	// literals joined by newlines, comments dropped. For a file that did not
	// parse as Go (including go.mod) it is the lowercased raw source and
	// Parsed is false.
	Code string
	// Funcs is the file's function declarations, in source order. Empty when
	// Parsed is false.
	Funcs []repoDecl
	// Values is the file's top-level const/var bindings. A directive written
	// once as a named constant and used by name is still that function's
	// directive, and this is what lets a check follow the name.
	Values []repoDecl
	// Stmts is the code text of each simple statement (a call, an assignment,
	// a return, a declaration). It is the finest granularity a check gets, and
	// it exists for the one question the coarser levels cannot answer: what a
	// helper is APPLIED to. mux.Handle("/static/", withCache(fs)) says what
	// withCache itself never does. A statement nested inside another appears
	// at both levels.
	Stmts []string
	// Maps reports whether the file declares a map type -- the tell of a
	// per-process, in-memory counter when it shows up in limiter code.
	Maps bool
	// Parsed reports whether Code came from the Go parser (comment-free) or
	// from the raw bytes.
	Parsed bool
}

// has reports the first of terms contained in the file's code text.
func (f repoFile) has(terms ...string) (string, bool) { return firstTerm(f.Code, terms...) }

// firstTerm returns the first of terms contained in a piece of code text.
func firstTerm(code string, terms ...string) (string, bool) {
	for _, t := range terms {
		if strings.Contains(code, t) {
			return t, true
		}
	}
	return "", false
}

// namesIdent reports whether code text references exactly this identifier. Code
// text is one token per line, so the comparison is line-exact: a substring match
// would make "cache" match "cacheHeaders" and attribute a helper's call sites to
// an unrelated one.
func namesIdent(code, name string) bool {
	if name == "" {
		return false
	}
	for line := range strings.SplitSeq(code, "\n") {
		if line == name {
			return true
		}
	}
	return false
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

// value returns the code text of the top-level const/var binding of that name,
// from anywhere in the tree (one package, so a sibling file counts).
func (t *repoTree) value(name string) (string, bool) {
	for _, f := range t.Files {
		for _, v := range f.Values {
			if v.Name == name {
				return v.Code, true
			}
		}
	}
	return "", false
}

// changed reports whether the run's diff shows the agent touched the repo at
// all. It is diagnostic only: an empty diff with a correct tree means the
// runner failed to capture the diff, not that nothing happened.
func (t *repoTree) changed() bool { return strings.TrimSpace(t.Diff) != "" }

// diffPaths lists the repo-relative paths the run's diff touches, from its
// "--- a/" and "+++ b/" header lines. Like changed, it reads the diff and is
// therefore for OBSERVED checks only: an incomplete diff must dull a
// measurement, never flip a verdict.
func (t *repoTree) diffPaths() []string {
	seen := map[string]bool{}
	var out []string
	for line := range strings.SplitSeq(t.Diff, "\n") {
		var p string
		switch {
		case strings.HasPrefix(line, "--- a/"):
			p = strings.TrimPrefix(line, "--- a/")
		case strings.HasPrefix(line, "+++ b/"):
			p = strings.TrimPrefix(line, "+++ b/")
		default:
			continue
		}
		// A tab can trail the path in traditional diff headers.
		p, _, _ = strings.Cut(p, "\t")
		if p == "" || p == "/dev/null" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// diffTouches reports whether the run's diff touches the exact path.
func (t *repoTree) diffTouches(path string) bool {
	return slices.Contains(t.diffPaths(), path)
}

// linesMatching returns the code-text lines anywhere in the tree that match
// re. Code text is one token per line -- an identifier, an import path, or a
// whole string literal -- so this is how a check matches the SHAPE of a
// literal (a content-hashed asset path) rather than the presence of a term.
func (t *repoTree) linesMatching(re *regexp.Regexp) []string {
	var out []string
	for _, f := range t.Files {
		for line := range strings.SplitSeq(f.Code, "\n") {
			if line != "" && re.MatchString(line) {
				out = append(out, line)
			}
		}
	}
	return out
}

// fileExists reports whether the preserved tree holds a regular file at the
// repo-relative path -- for checks on files the token scan does not read
// (static assets), where "the link points at something real" is the question.
func (t *repoTree) fileExists(rel string) bool {
	info, err := os.Lstat(filepath.Join(t.Root, filepath.FromSlash(rel)))
	return err == nil && info.Mode().IsRegular()
}

// filesUnder lists the preserved tree's file names under a repo-relative
// directory (non-recursive), token-scanned or not.
func (t *repoTree) filesUnder(rel string) []string {
	entries, err := os.ReadDir(filepath.Join(t.Root, filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			out = append(out, e.Name())
		}
	}
	return out
}

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
	if strings.HasSuffix(rel, ".go") {
		if f, ok := goScan(src); ok {
			f.Path = rel
			return f
		}
	}
	return repoFile{Path: rel, Code: strings.ToLower(string(src))}
}

// goScan reduces a Go file to its matchable forms: the whole-file code text,
// each function declaration, each top-level const/var binding, each simple
// statement, and whether the file declares a map type. Parsing without
// ParseComments is what drops comments: they never enter the AST, so no comment
// text can be mistaken for code.
func goScan(src []byte) (repoFile, bool) {
	file, err := parser.ParseFile(token.NewFileSet(), "", src, 0)
	if err != nil {
		return repoFile{}, false
	}
	f := repoFile{Parsed: true}
	f.Code, f.Maps = nodeCode(file)
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			code, _ := nodeCode(d)
			f.Funcs = append(f.Funcs, repoDecl{Name: strings.ToLower(d.Name.Name), Code: code})
		case *ast.GenDecl:
			if d.Tok != token.CONST && d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				code, _ := nodeCode(vs)
				for _, name := range vs.Names {
					f.Values = append(f.Values, repoDecl{Name: strings.ToLower(name.Name), Code: code})
				}
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.AssignStmt, *ast.DeclStmt, *ast.DeferStmt, *ast.ExprStmt, *ast.GoStmt, *ast.ReturnStmt:
			code, _ := nodeCode(n)
			f.Stmts = append(f.Stmts, code)
		}
		return true
	})
	return f, true
}

// nodeCode extracts one AST node's identifiers, import paths, and string
// literals, lowercased and newline-joined, reporting whether it declares a map
// type.
func nodeCode(n ast.Node) (code string, maps bool) {
	var b strings.Builder
	ast.Inspect(n, func(n ast.Node) bool {
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
	return b.String(), maps
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
