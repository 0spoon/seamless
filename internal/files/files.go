package files

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/validate"
)

// ErrTreeEscape is returned when an item's computed file path would land
// outside its own tree: a memory outside memory/, or a note outside notes/. A
// hostile or corrupt project value (e.g. "../notes") cleans to a path that
// stays inside the data dir -- which the traversal guard accepts -- but crosses
// into the other tree; this containment check is the files-layer backstop
// behind the MCP layer's project validation.
var ErrTreeEscape = errors.New("computed path escapes the item's tree")

// checkTree verifies that an item's project is a safe single path segment (the
// same validate.Name rule the MCP layer applies to explicit project args) and
// that the computed data-dir-relative path sits under the expected tree
// ("memory" or "notes"). The project check catches hostile values like
// "../notes" directly; the path check is the invariant backstop should path
// construction ever change.
func checkTree(project, relPath, wantTree string) error {
	if project != "" {
		if err := validate.Name(project); err != nil {
			return fmt.Errorf("%w: project: %w", ErrTreeEscape, err)
		}
	}
	if tree, _, ok := treeAndRel(filepath.ToSlash(filepath.Clean(filepath.FromSlash(relPath)))); !ok || tree != wantTree {
		return fmt.Errorf("%w: %q is not under %s/", ErrTreeEscape, relPath, wantTree)
	}
	return nil
}

// Tree names, used as the first path segment of a data-dir-relative file_path
// and as the on-disk directory for items with no project.
const (
	memoryTree      = "memory"
	notesTree       = "notes"
	memoryGlobalDir = "_global" // memory/{_global} holds project-less memories
	notesGlobalDir  = "_global" // notes/{_global} holds project-less notes
	fileMode        = 0o644
)

// notesLegacyGlobalDir is what notesGlobalDir used to be called. Project-less
// notes are "global" everywhere an agent can see -- notes_create project=global,
// capture_url project=global, memory/_global -- but they landed in notes/inbox,
// so the one name nobody said out loud was the one on disk. That drift is why
// seam capture's own --project help claimed "empty = inbox" when empty actually
// means the session's project. Kept only for the one-time move in Start.
const notesLegacyGlobalDir = "inbox"

// ContentHash returns the SHA-256 hex digest of a file's full content. It is the
// change-detection key the reconciler compares against the index.
func ContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", sum)
}

// MemoryRelPath returns the data-dir-relative path of a memory file:
// memory/{project|_global}/{name}.md.
func MemoryRelPath(project, name string) string {
	dir := project
	if dir == "" {
		dir = memoryGlobalDir
	}
	return filepath.ToSlash(filepath.Join(memoryTree, dir, name+".md"))
}

// NoteRelPath returns the data-dir-relative path of a note file:
// notes/{project|_global}/{slug}.md.
func NoteRelPath(project, slug string) string {
	dir := project
	if dir == "" {
		dir = notesGlobalDir
	}
	return filepath.ToSlash(filepath.Join(notesTree, dir, slug+".md"))
}

// rfc formats a timestamp for frontmatter (RFC3339, UTC). Zero yields "".
func rfc(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ---------------------------------------------------------------------------
// Memory parse / render
// ---------------------------------------------------------------------------

// RenderMemory serializes a memory to full markdown file content.
func RenderMemory(m core.Memory) (string, error) {
	fm := memoryFrontmatter{
		ID:            m.ID,
		Kind:          string(m.Kind),
		Name:          m.Name,
		Description:   m.Description,
		Project:       m.Project,
		Created:       rfc(m.Created),
		Updated:       rfc(m.Updated),
		ValidFrom:     rfc(m.ValidFrom),
		SupersededBy:  m.SupersededBy,
		SourceSession: m.SourceSession,
		Model:         m.Model,
		Tags:          m.Tags,
		Extra:         m.Extra,
	}
	if m.InvalidAt != nil {
		fm.InvalidAt = rfc(*m.InvalidAt)
	}
	data, err := yaml.Marshal(&fm)
	if err != nil {
		return "", fmt.Errorf("files.RenderMemory: %w", err)
	}
	return wrapDocument(data, m.Body), nil
}

// ParseMemory parses memory file content into a core.Memory. relPath is the
// data-dir-relative file path recorded on the result.
func ParseMemory(content, relPath string) (core.Memory, error) {
	yamlText, body := splitDocument(content)
	var fm memoryFrontmatter
	if yamlText != "" {
		if err := yaml.Unmarshal([]byte(yamlText), &fm); err != nil {
			return core.Memory{}, fmt.Errorf("files.ParseMemory: %s: %w", relPath, err)
		}
	}

	created, err := core.ParseTime(fm.Created)
	if err != nil {
		return core.Memory{}, fmt.Errorf("files.ParseMemory: %s: created: %w", relPath, err)
	}
	updated, err := core.ParseTime(fm.Updated)
	if err != nil {
		return core.Memory{}, fmt.Errorf("files.ParseMemory: %s: updated: %w", relPath, err)
	}
	validFrom, err := core.ParseTime(fm.ValidFrom)
	if err != nil {
		return core.Memory{}, fmt.Errorf("files.ParseMemory: %s: valid_from: %w", relPath, err)
	}
	var invalidAt *time.Time
	if fm.InvalidAt != "" {
		t, err := core.ParseTime(fm.InvalidAt)
		if err != nil {
			return core.Memory{}, fmt.Errorf("files.ParseMemory: %s: invalid_at: %w", relPath, err)
		}
		invalidAt = &t
	}

	return core.Memory{
		ID:            fm.ID,
		Kind:          core.MemoryKind(fm.Kind),
		Name:          fm.Name,
		Description:   fm.Description,
		Project:       fm.Project,
		Body:          body,
		FilePath:      relPath,
		Tags:          fm.Tags,
		Created:       created,
		Updated:       updated,
		ValidFrom:     validFrom,
		InvalidAt:     invalidAt,
		SupersededBy:  fm.SupersededBy,
		SourceSession: fm.SourceSession,
		Model:         fm.Model,
		ContentHash:   ContentHash(content),
		Extra:         fm.Extra,
	}, nil
}

// ---------------------------------------------------------------------------
// Note parse / render
// ---------------------------------------------------------------------------

// RenderNote serializes a note to full markdown file content.
func RenderNote(n core.Note) (string, error) {
	fm := noteFrontmatter{
		ID:          n.ID,
		Title:       n.Title,
		Slug:        n.Slug,
		Description: n.Description,
		Project:     n.Project,
		Created:     rfc(n.Created),
		Updated:     rfc(n.Updated),
		SourceURL:   n.SourceURL,
		Model:       n.Model,
		Tags:        n.Tags,
		Extra:       n.Extra,
	}
	data, err := yaml.Marshal(&fm)
	if err != nil {
		return "", fmt.Errorf("files.RenderNote: %w", err)
	}
	return wrapDocument(data, n.Body), nil
}

// ParseNote parses note file content into a core.Note.
func ParseNote(content, relPath string) (core.Note, error) {
	yamlText, body := splitDocument(content)
	var fm noteFrontmatter
	if yamlText != "" {
		if err := yaml.Unmarshal([]byte(yamlText), &fm); err != nil {
			return core.Note{}, fmt.Errorf("files.ParseNote: %s: %w", relPath, err)
		}
	}

	created, err := core.ParseTime(fm.Created)
	if err != nil {
		return core.Note{}, fmt.Errorf("files.ParseNote: %s: created: %w", relPath, err)
	}
	updated, err := core.ParseTime(fm.Updated)
	if err != nil {
		return core.Note{}, fmt.Errorf("files.ParseNote: %s: updated: %w", relPath, err)
	}

	return core.Note{
		ID:          fm.ID,
		Title:       fm.Title,
		Slug:        fm.Slug,
		Description: fm.Description,
		Project:     fm.Project,
		Body:        body,
		FilePath:    relPath,
		Tags:        fm.Tags,
		SourceURL:   fm.SourceURL,
		Model:       fm.Model,
		Created:     created,
		Updated:     updated,
		ContentHash: ContentHash(content),
		Extra:       fm.Extra,
	}, nil
}

// ---------------------------------------------------------------------------
// Store: pure filesystem read/write of the two trees under a data dir
// ---------------------------------------------------------------------------

// Store reads and writes memory and note files under a data directory. It owns
// no database; the index mirror and watcher are layered on top.
type Store struct {
	dataDir string
}

// NewStore returns a Store rooted at dataDir.
func NewStore(dataDir string) *Store { return &Store{dataDir: dataDir} }

// DataDir returns the store's root directory.
func (s *Store) DataDir() string { return s.dataDir }

// abs resolves a data-dir-relative path to an absolute path, rejecting escapes.
func (s *Store) abs(relPath string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relPath))
	if err := validate.PathWithinDir(clean, s.dataDir); err != nil {
		return "", fmt.Errorf("files: %s: %w", relPath, err)
	}
	return filepath.Join(s.dataDir, clean), nil
}

// WriteMemory renders m, writes it atomically to its computed path, and returns
// m updated with FilePath and ContentHash. Name must be a safe filename.
func (s *Store) WriteMemory(m core.Memory) (core.Memory, error) {
	if err := validate.Name(m.Name); err != nil {
		return core.Memory{}, fmt.Errorf("files.WriteMemory: name: %w", err)
	}
	m.FilePath = MemoryRelPath(m.Project, m.Name)
	if err := checkTree(m.Project, m.FilePath, memoryTree); err != nil {
		return core.Memory{}, fmt.Errorf("files.WriteMemory: %w", err)
	}
	content, err := RenderMemory(m)
	if err != nil {
		return core.Memory{}, err
	}
	if err := s.writeFile(m.FilePath, content); err != nil {
		return core.Memory{}, fmt.Errorf("files.WriteMemory: %w", err)
	}
	m.ContentHash = ContentHash(content)
	return m, nil
}

// ReadMemory reads and parses the memory at a data-dir-relative path.
func (s *Store) ReadMemory(relPath string) (core.Memory, error) {
	content, err := s.readFile(relPath)
	if err != nil {
		return core.Memory{}, fmt.Errorf("files.ReadMemory: %w", err)
	}
	return ParseMemory(content, relPath)
}

// WriteNote renders n, writes it atomically, and returns n updated with FilePath
// and ContentHash. Slug must be a safe filename.
func (s *Store) WriteNote(n core.Note) (core.Note, error) {
	if err := validate.Name(n.Slug); err != nil {
		return core.Note{}, fmt.Errorf("files.WriteNote: slug: %w", err)
	}
	n.FilePath = NoteRelPath(n.Project, n.Slug)
	if err := checkTree(n.Project, n.FilePath, notesTree); err != nil {
		return core.Note{}, fmt.Errorf("files.WriteNote: %w", err)
	}
	content, err := RenderNote(n)
	if err != nil {
		return core.Note{}, err
	}
	if err := s.writeFile(n.FilePath, content); err != nil {
		return core.Note{}, fmt.Errorf("files.WriteNote: %w", err)
	}
	n.ContentHash = ContentHash(content)
	return n, nil
}

// ReadNote reads and parses the note at a data-dir-relative path.
func (s *Store) ReadNote(relPath string) (core.Note, error) {
	content, err := s.readFile(relPath)
	if err != nil {
		return core.Note{}, fmt.Errorf("files.ReadNote: %w", err)
	}
	return ParseNote(content, relPath)
}

// Remove deletes the file at a data-dir-relative path. A missing file is not an
// error (the desired end-state already holds).
func (s *Store) Remove(relPath string) error {
	abs, err := s.abs(relPath)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("files.Remove: %w", err)
	}
	return nil
}

// Exists reports whether the file at a data-dir-relative path exists.
func (s *Store) Exists(relPath string) bool {
	abs, err := s.abs(relPath)
	if err != nil {
		return false
	}
	_, err = os.Stat(abs)
	return err == nil
}

func (s *Store) readFile(relPath string) (string, error) {
	abs, err := s.abs(relPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Store) writeFile(relPath, content string) error {
	abs, err := s.abs(relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("files: mkdir: %w", err)
	}
	return AtomicWrite(abs, []byte(content), fileMode)
}

// treeAndRel splits a data-dir-relative path into its tree ("memory"|"notes")
// and the remainder. It reports ok=false for paths outside a known tree.
func treeAndRel(relPath string) (tree, rest string, ok bool) {
	relPath = filepath.ToSlash(relPath)
	switch {
	case strings.HasPrefix(relPath, memoryTree+"/"):
		return memoryTree, strings.TrimPrefix(relPath, memoryTree+"/"), true
	case strings.HasPrefix(relPath, notesTree+"/"):
		return notesTree, strings.TrimPrefix(relPath, notesTree+"/"), true
	default:
		return "", "", false
	}
}
