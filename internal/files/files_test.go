package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

func TestMemoryRelPath(t *testing.T) {
	require.Equal(t, "memory/seam/chroma-boot-race.md", MemoryRelPath("seam", "chroma-boot-race"))
	require.Equal(t, "memory/_global/ulid-only.md", MemoryRelPath("", "ulid-only"))
}

func TestNoteRelPath(t *testing.T) {
	require.Equal(t, "notes/seam/landscape-scan.md", NoteRelPath("seam", "landscape-scan"))
	require.Equal(t, "notes/inbox/stray-thought.md", NoteRelPath("", "stray-thought"))
}

func sampleMemory() core.Memory {
	return core.Memory{
		ID:            "01K0MEMORY000000000000000A",
		Kind:          core.KindGotcha,
		Name:          "chroma-boot-race",
		Description:   "chroma races the daemon on boot; add a readiness gate",
		Project:       "seam",
		Body:          "The chroma sidecar answers /heartbeat before it can serve queries.\n",
		Tags:          []string{"chroma", "boot"},
		Created:       time.Date(2026, 7, 10, 18, 0, 0, 0, time.UTC),
		Updated:       time.Date(2026, 7, 10, 18, 30, 0, 0, time.UTC),
		ValidFrom:     time.Date(2026, 7, 10, 18, 0, 0, 0, time.UTC),
		SourceSession: "cc/ab12cd34",
	}
}

func TestMemoryRoundTrip(t *testing.T) {
	m := sampleMemory()
	content, err := RenderMemory(m)
	require.NoError(t, err)

	got, err := ParseMemory(content, MemoryRelPath(m.Project, m.Name))
	require.NoError(t, err)

	require.Equal(t, m.ID, got.ID)
	require.Equal(t, m.Kind, got.Kind)
	require.Equal(t, m.Name, got.Name)
	require.Equal(t, m.Description, got.Description)
	require.Equal(t, m.Project, got.Project)
	require.Equal(t, m.Body, got.Body)
	require.Equal(t, m.Tags, got.Tags)
	require.True(t, m.Created.Equal(got.Created))
	require.True(t, m.Updated.Equal(got.Updated))
	require.True(t, m.ValidFrom.Equal(got.ValidFrom))
	require.Nil(t, got.InvalidAt)
	require.Empty(t, got.SupersededBy)
	require.Equal(t, m.SourceSession, got.SourceSession)
	require.Equal(t, "memory/seam/chroma-boot-race.md", got.FilePath)
}

// Rendering twice must be byte-identical (stable output => clean git diffs).
func TestMemoryRenderIsStable(t *testing.T) {
	m := sampleMemory()
	first, err := RenderMemory(m)
	require.NoError(t, err)

	parsed, err := ParseMemory(first, "memory/seam/chroma-boot-race.md")
	require.NoError(t, err)
	second, err := RenderMemory(parsed)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

// Lifecycle fields are always emitted (as null when unset) so an editor can see
// they exist; a superseded memory round-trips its invalid_at + superseded_by.
func TestMemoryLifecycleFields(t *testing.T) {
	m := sampleMemory()
	rendered, err := RenderMemory(m)
	require.NoError(t, err)
	require.Contains(t, rendered, "invalid_at: null")
	require.Contains(t, rendered, "superseded_by: null")

	invalid := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	m.InvalidAt = &invalid
	m.SupersededBy = "01K0REPLACEMENT0000000000B"
	rendered, err = RenderMemory(m)
	require.NoError(t, err)
	require.NotContains(t, rendered, "invalid_at: null")

	got, err := ParseMemory(rendered, "memory/seam/chroma-boot-race.md")
	require.NoError(t, err)
	require.NotNil(t, got.InvalidAt)
	require.True(t, invalid.Equal(*got.InvalidAt))
	require.Equal(t, "01K0REPLACEMENT0000000000B", got.SupersededBy)
	require.False(t, got.Active())
}

// A project-less memory omits the project line and parses back as global.
func TestGlobalMemoryOmitsProject(t *testing.T) {
	m := sampleMemory()
	m.Project = ""
	rendered, err := RenderMemory(m)
	require.NoError(t, err)
	require.NotContains(t, rendered, "project:")

	got, err := ParseMemory(rendered, MemoryRelPath("", m.Name))
	require.NoError(t, err)
	require.Empty(t, got.Project)
}

// Unknown YAML keys (e.g. an Obsidian plugin field) survive a round-trip.
func TestFrontmatterPreservesUnknownKeys(t *testing.T) {
	content := `---
id: 01K0MEMORY000000000000000A
kind: gotcha
name: chroma-boot-race
description: something
created: 2026-07-10T18:00:00Z
updated: 2026-07-10T18:00:00Z
valid_from: 2026-07-10T18:00:00Z
invalid_at: null
superseded_by: null
obsidian_color: "#ff8800"
cssclass: memory
---
body text here
`
	got, err := ParseMemory(content, "memory/_global/chroma-boot-race.md")
	require.NoError(t, err)
	require.Equal(t, "body text here\n", got.Body)

	rendered, err := RenderMemory(got)
	require.NoError(t, err)
	require.Contains(t, rendered, "obsidian_color:")
	require.Contains(t, rendered, "cssclass: memory")
}

func TestNoteRoundTrip(t *testing.T) {
	n := core.Note{
		ID:          "01K0NOTE00000000000000000A",
		Title:       "Landscape scan 2026-07",
		Slug:        "landscape-scan",
		Description: "survey of agent-memory systems",
		Project:     "seam",
		Body:        "# Findings\n\nMem0 delta was ~2%.\n",
		Tags:        []string{"research"},
		SourceURL:   "https://example.com/scan",
		Created:     time.Date(2026, 7, 9, 2, 0, 0, 0, time.UTC),
		Updated:     time.Date(2026, 7, 9, 3, 0, 0, 0, time.UTC),
	}
	content, err := RenderNote(n)
	require.NoError(t, err)

	got, err := ParseNote(content, NoteRelPath(n.Project, n.Slug))
	require.NoError(t, err)
	require.Equal(t, n.ID, got.ID)
	require.Equal(t, n.Title, got.Title)
	require.Equal(t, n.Slug, got.Slug)
	require.Equal(t, n.Description, got.Description)
	require.Equal(t, n.Project, got.Project)
	require.Equal(t, n.Body, got.Body)
	require.Equal(t, n.Tags, got.Tags)
	require.Equal(t, n.SourceURL, got.SourceURL)
	require.True(t, n.Created.Equal(got.Created))
	require.Equal(t, "notes/seam/landscape-scan.md", got.FilePath)
}

// A file with no frontmatter parses as an all-body note (import normalizes it).
func TestParseNoteNoFrontmatter(t *testing.T) {
	got, err := ParseNote("just a plain body\nno frontmatter\n", "notes/inbox/plain.md")
	require.NoError(t, err)
	require.Empty(t, got.ID)
	require.Equal(t, "just a plain body\nno frontmatter\n", got.Body)
}

func TestContentHashStable(t *testing.T) {
	require.Equal(t, ContentHash("hello"), ContentHash("hello"))
	require.NotEqual(t, ContentHash("hello"), ContentHash("world"))
	require.Len(t, ContentHash("x"), 64) // sha256 hex
}

func TestStoreWriteReadMemory(t *testing.T) {
	s := NewStore(t.TempDir())
	m := sampleMemory()

	written, err := s.WriteMemory(m)
	require.NoError(t, err)
	require.Equal(t, "memory/seam/chroma-boot-race.md", written.FilePath)
	require.NotEmpty(t, written.ContentHash)
	require.True(t, s.Exists(written.FilePath))

	got, err := s.ReadMemory(written.FilePath)
	require.NoError(t, err)
	require.Equal(t, m.Name, got.Name)
	require.Equal(t, m.Body, got.Body)
	require.Equal(t, written.ContentHash, got.ContentHash)

	require.NoError(t, s.Remove(written.FilePath))
	require.False(t, s.Exists(written.FilePath))
	require.NoError(t, s.Remove(written.FilePath)) // idempotent
}

func TestStoreWriteNote(t *testing.T) {
	s := NewStore(t.TempDir())
	n := core.Note{
		ID:      "01K0NOTE00000000000000000A",
		Title:   "Stray thought",
		Slug:    "stray-thought",
		Body:    "no project => inbox\n",
		Created: time.Date(2026, 7, 9, 2, 0, 0, 0, time.UTC),
		Updated: time.Date(2026, 7, 9, 2, 0, 0, 0, time.UTC),
	}
	written, err := s.WriteNote(n)
	require.NoError(t, err)
	require.Equal(t, "notes/inbox/stray-thought.md", written.FilePath)
	require.True(t, s.Exists("notes/inbox/stray-thought.md"))
}

// Unsafe names must be rejected before any filesystem write.
func TestStoreRejectsUnsafeName(t *testing.T) {
	s := NewStore(t.TempDir())
	m := sampleMemory()
	m.Name = "../escape"
	_, err := s.WriteMemory(m)
	require.Error(t, err)
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "f.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	require.NoError(t, AtomicWrite(path, []byte("v1\n"), 0o644))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "v1\n", string(data))

	// Overwrite is atomic and leaves no temp files behind.
	require.NoError(t, AtomicWrite(path, []byte("v2\n"), 0o644))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "v2\n", string(data))

	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, strings.HasPrefix(e.Name(), ".seamless-tmp-"), "temp file left behind: %s", e.Name())
	}
}
